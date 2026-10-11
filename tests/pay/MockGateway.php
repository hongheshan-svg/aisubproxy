<?php
declare(strict_types=1);

/**
 * 支付网关模拟器（只用于本地端到端测试，配合 tests/pay/e2e.php）。
 *
 *   php -S 127.0.0.1:9100 tests/pay/MockGateway.php
 *
 * 模拟易支付、支付宝开放平台、微信支付 APIv3、BEpusdt / GM Pay 四类网关：
 * 校验站点发来的下单请求签名、返回带签名的应答、按各平台格式发送签名正确（或故意篡改）的异步通知。
 * 签名/加密全部独立实现，不引用 app/Pay 下的代码，这样才能交叉验证插件实现是否正确。
 *
 * 密钥与配置由 e2e.php 写入 $MOCK_DIR/keys.json，已收到的下单记录存在 $MOCK_DIR/orders.json。
 */

const MOCK_DIR = '/tmp/acg-pay-mock';

$keys = json_decode((string)@file_get_contents(MOCK_DIR . '/keys.json'), true) ?: [];
$path = (string)parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH);
$raw = (string)file_get_contents('php://input');

function orders(): array
{
    return json_decode((string)@file_get_contents(MOCK_DIR . '/orders.json'), true) ?: [];
}

function remember(string $tradeNo, array $order): void
{
    $all = orders();
    $all[$tradeNo] = $order;
    file_put_contents(MOCK_DIR . '/orders.json', json_encode($all, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES));
}

function reply(int $status, string $body, array $headers = []): void
{
    http_response_code($status);
    foreach ($headers as $k => $v) {
        header("{$k}: {$v}");
    }
    echo $body;
}

function json_reply(array $data, int $status = 200): void
{
    reply($status, json_encode($data, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES), ['Content-Type' => 'application/json']);
}

/** 排序拼接：排除字段与空值，k=v&k=v；数字按 Go 写法（10 而不是 10.0） */
function canonical(array $p, array $exclude): string
{
    ksort($p, SORT_STRING);
    $out = [];
    foreach ($p as $k => $v) {
        if (in_array($k, $exclude, true) || $v === null || $v === '' || is_array($v)) {
            continue;
        }
        if (is_float($v)) {
            $v = (floor($v) == $v) ? sprintf('%d', $v) : rtrim(rtrim(sprintf('%.8F', $v), '0'), '.');
        } elseif (is_bool($v)) {
            $v = $v ? 'true' : 'false';
        }
        $out[] = "{$k}={$v}";
    }
    return implode('&', $out);
}

function rsa_sign(string $data, string $privatePem): string
{
    openssl_sign($data, $sig, $privatePem, OPENSSL_ALGO_SHA256);
    return base64_encode($sig);
}

function rsa_verify(string $data, string $sigB64, string $publicPem): bool
{
    return openssl_verify($data, (string)base64_decode($sigB64), $publicPem, OPENSSL_ALGO_SHA256) === 1;
}

/** 把站点给的回调地址发出去（GET 带 query / POST 表单 / POST JSON） */
function deliver(string $method, string $url, string|array $body, array $headers = []): array
{
    $ch = curl_init();
    if ($method === 'GET') {
        $url .= (str_contains($url, '?') ? '&' : '?') . http_build_query((array)$body);
    } else {
        curl_setopt($ch, CURLOPT_POST, true);
        curl_setopt($ch, CURLOPT_POSTFIELDS, is_array($body) ? http_build_query($body) : $body);
    }
    $hdr = [];
    foreach ($headers as $k => $v) {
        $hdr[] = "{$k}: {$v}";
    }
    curl_setopt_array($ch, [CURLOPT_URL => $url, CURLOPT_RETURNTRANSFER => true, CURLOPT_HTTPHEADER => $hdr, CURLOPT_TIMEOUT => 15]);
    $resp = curl_exec($ch);
    $code = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['http_code' => $code, 'body' => (string)$resp];
}

// ---------------------------------------------------------------- 易支付
if ($path === '/epay/submit.php' || $path === '/epay/mapi.php') {
    $p = $_POST ?: $_GET;
    $expect = md5(canonical($p, ['sign', 'sign_type']) . $keys['epay_key']);
    if (($p['sign'] ?? '') !== $expect || (string)($p['pid'] ?? '') !== (string)$keys['epay_pid']) {
        $path === '/epay/mapi.php' ? json_reply(['code' => -1, 'msg' => '签名错误']) : reply(400, 'sign error');
        return;
    }
    remember($p['out_trade_no'], ['gw' => 'epay', 'money' => $p['money'], 'type' => $p['type'], 'notify_url' => $p['notify_url'], 'name' => $p['name']]);
    if ($path === '/epay/submit.php') {
        reply(200, '<html><body>MOCK EPAY CASHIER ' . htmlspecialchars($p['out_trade_no']) . '</body></html>');
    } else {
        json_reply(['code' => 1, 'msg' => 'ok', 'trade_no' => 'EP' . $p['out_trade_no'], 'qrcode' => 'weixin://wxpay/bizpayurl?pr=mockepay' . $p['out_trade_no']]);
    }
    return;
}

// ---------------------------------------------------------------- 支付宝
if ($path === '/alipay/gateway.do') {
    $p = $_POST;
    if (!rsa_verify(canonical($p, ['sign']), (string)($p['sign'] ?? ''), $keys['alipay_app_public'])) {
        json_reply(['error_response' => ['code' => '40002', 'msg' => 'Invalid Arguments', 'sub_code' => 'isv.invalid-signature', 'sub_msg' => '验签出错']]);
        return;
    }
    $biz = json_decode((string)$p['biz_content'], true);
    remember($biz['out_trade_no'], ['gw' => 'alipay', 'total_amount' => $biz['total_amount'], 'notify_url' => $p['notify_url'], 'app_id' => $p['app_id'], 'method' => $p['method']]);

    if ($p['method'] === 'alipay.trade.precreate') {
        $node = json_encode(['code' => '10000', 'msg' => 'Success', 'out_trade_no' => $biz['out_trade_no'], 'qr_code' => 'https://qr.alipay.com/mock' . $biz['out_trade_no']], JSON_UNESCAPED_SLASHES);
        $sign = rsa_sign($node, $keys['alipay_platform_private']);
        reply(200, '{"alipay_trade_precreate_response":' . $node . ',"sign":"' . $sign . '"}', ['Content-Type' => 'text/html;charset=utf-8']);
    } else {
        reply(200, '<html><body>MOCK ALIPAY CASHIER ' . htmlspecialchars($p['method']) . '</body></html>');
    }
    return;
}

// ---------------------------------------------------------------- 微信支付 APIv3
if (str_starts_with($path, '/wechat/v3/pay/transactions/')) {
    $auth = (string)($_SERVER['HTTP_AUTHORIZATION'] ?? '');
    preg_match_all('/(\w+)="([^"]*)"/', $auth, $m);
    $a = array_combine($m[1], $m[2]);
    $apiPath = substr($path, strlen('/wechat'));
    $message = "POST\n{$apiPath}\n{$a['timestamp']}\n{$a['nonce_str']}\n{$raw}\n";
    if (!str_starts_with($auth, 'WECHATPAY2-SHA256-RSA2048 ') || !rsa_verify($message, $a['signature'] ?? '', $keys['wechat_merchant_public'])
        || ($a['serial_no'] ?? '') !== $keys['wechat_merchant_serial']) {
        json_reply(['code' => 'SIGN_ERROR', 'message' => '签名错误'], 401);
        return;
    }
    $req = json_decode($raw, true);
    remember($req['out_trade_no'], ['gw' => 'wechat', 'total' => $req['amount']['total'], 'notify_url' => $req['notify_url'], 'appid' => $req['appid'], 'mchid' => $req['mchid']]);

    $body = str_ends_with($path, '/native')
        ? json_encode(['code_url' => 'weixin://wxpay/bizpayurl?pr=mock' . $req['out_trade_no']])
        : json_encode(['h5_url' => 'https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=wxmock' . $req['out_trade_no'] . '&package=1037687096'], JSON_UNESCAPED_SLASHES);
    $ts = (string)time();
    $nonce = bin2hex(random_bytes(8));
    reply(200, $body, [
        'Content-Type' => 'application/json',
        'Wechatpay-Timestamp' => $ts,
        'Wechatpay-Nonce' => $nonce,
        'Wechatpay-Serial' => $keys['wechat_platform_serial'],
        'Wechatpay-Signature' => rsa_sign("{$ts}\n{$nonce}\n{$body}\n", $keys['wechat_platform_private']),
    ]);
    return;
}

// ---------------------------------------------------------------- BEpusdt / GM Pay
if (preg_match('#^/usdt/(api/v1|payments/gmpay/v1)/order/create-(transaction|order)$#', $path, $mm)) {
    $p = json_decode($raw, true) ?: [];
    $gm = $mm[1] === 'payments/gmpay/v1';
    $expect = $gm
        ? hash_hmac('sha256', canonical($p, ['signature']), $keys['gm_secret'])
        : md5(canonical($p, ['signature']) . $keys['usdt_token']);
    if (($p['signature'] ?? '') !== $expect || ($gm && ($p['pid'] ?? '') !== $keys['gm_pid'])) {
        json_reply(['status_code' => 401, 'message' => '签名认证错误', 'data' => null]);
        return;
    }
    $tradeId = 'mock-' . $p['order_id'];
    remember($p['order_id'], ['gw' => $gm ? 'gmpay' : 'usdt', 'amount' => $p['amount'], 'notify_url' => $p['notify_url'], 'trade_id' => $tradeId,
        'trade_type' => $p['trade_type'] ?? '', 'endpoint' => $mm[2]]);
    json_reply(['status_code' => 200, 'message' => 'success', 'data' => [
        'trade_id' => $tradeId, 'order_id' => $p['order_id'], 'amount' => (string)$p['amount'], 'actual_amount' => '1.01',
        'token' => 'TMockReceiveAddress', 'expiration_time' => 600, 'status' => 1,
        'payment_url' => 'http://127.0.0.1:9100/usdt/pay/checkout-counter/' . $tradeId,
    ]]);
    return;
}

// ---------------------------------------------------------------- 测试控制：发送异步通知
// /_mock/notify?order=单号[&tamper=1][&amount=x][&finished=1]
if ($path === '/_mock/notify') {
    $no = (string)($_GET['order'] ?? '');
    $o = orders()[$no] ?? null;
    if (!$o) {
        json_reply(['error' => 'unknown order'], 404);
        return;
    }
    $tamper = !empty($_GET['tamper']);
    $amount = $_GET['amount'] ?? null;

    switch ($o['gw']) {
        case 'epay':
            $n = ['pid' => $keys['epay_pid'], 'trade_no' => 'EP' . $no, 'out_trade_no' => $no, 'type' => $o['type'], 'name' => $o['name'],
                'money' => $amount ?? $o['money'], 'trade_status' => 'TRADE_SUCCESS', 'param' => ''];
            $n['sign'] = md5(canonical($n, ['sign', 'sign_type']) . $keys['epay_key']);
            $n['sign_type'] = 'MD5';
            if ($tamper) {
                $n['money'] = '0.01';
            }
            json_reply(deliver('GET', $o['notify_url'], $n));
            return;

        case 'alipay':
            $n = ['gmt_create' => date('Y-m-d H:i:s'), 'charset' => 'utf-8', 'subject' => '订单' . $no, 'buyer_id' => '2088000000000000',
                'invoice_amount' => $o['total_amount'], 'notify_id' => bin2hex(random_bytes(8)), 'notify_type' => 'trade_status_sync',
                'trade_status' => !empty($_GET['finished']) ? 'TRADE_FINISHED' : 'TRADE_SUCCESS', 'receipt_amount' => $o['total_amount'],
                'app_id' => $o['app_id'], 'seller_id' => '2088111111111111', 'gmt_payment' => date('Y-m-d H:i:s'),
                'notify_time' => date('Y-m-d H:i:s'), 'version' => '1.0', 'out_trade_no' => $no,
                'total_amount' => $amount ?? $o['total_amount'], 'trade_no' => '2026' . $no, 'auth_app_id' => $o['app_id'],
                'buyer_logon_id' => 'tes***@example.com', 'point_amount' => '0.00', 'fund_bill_list' => '[{"amount":"' . $o['total_amount'] . '","fundChannel":"ALIPAYACCOUNT"}]'];
            $n['sign'] = rsa_sign(canonical($n, ['sign', 'sign_type']), $keys['alipay_platform_private']);
            $n['sign_type'] = 'RSA2';
            if ($tamper) {
                $n['total_amount'] = '0.01';
            }
            json_reply(deliver('POST', $o['notify_url'], $n, ['Content-Type' => 'application/x-www-form-urlencoded; charset=utf-8']));
            return;

        case 'wechat':
            $total = $amount !== null ? (int)round((float)$amount * 100) : $o['total'];
            $trans = json_encode(['mchid' => $o['mchid'], 'appid' => $o['appid'], 'out_trade_no' => $no, 'transaction_id' => '4200' . $no,
                'trade_type' => 'NATIVE', 'trade_state' => 'SUCCESS', 'trade_state_desc' => '支付成功', 'bank_type' => 'OTHERS', 'attach' => '',
                'success_time' => date('c'), 'payer' => ['openid' => 'oMockOpenId'],
                'amount' => ['total' => $total, 'payer_total' => $total, 'currency' => 'CNY', 'payer_currency' => 'CNY']], JSON_UNESCAPED_UNICODE);
            $nonce = substr(bin2hex(random_bytes(6)), 0, 12);
            $cipher = openssl_encrypt($trans, 'aes-256-gcm', $keys['wechat_apiv3_key'], OPENSSL_RAW_DATA, $nonce, $tag, 'transaction');
            $body = json_encode(['id' => bin2hex(random_bytes(8)), 'create_time' => date('c'), 'resource_type' => 'encrypt-resource',
                'event_type' => 'TRANSACTION.SUCCESS', 'summary' => '支付成功', 'resource' => ['original_type' => 'transaction',
                    'algorithm' => 'AEAD_AES_256_GCM', 'ciphertext' => base64_encode($cipher . $tag), 'associated_data' => 'transaction', 'nonce' => $nonce]],
                JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
            $ts = (string)time();
            $hn = bin2hex(random_bytes(8));
            $sig = rsa_sign("{$ts}\n{$hn}\n{$body}\n", $keys['wechat_platform_private']);
            if ($tamper) {
                $sig = base64_encode(random_bytes(256));
            }
            json_reply(deliver('POST', $o['notify_url'], $body, ['Content-Type' => 'application/json', 'Wechatpay-Timestamp' => $ts,
                'Wechatpay-Nonce' => $hn, 'Wechatpay-Signature' => $sig, 'Wechatpay-Serial' => $keys['wechat_platform_serial']]));
            return;

        case 'usdt':
        case 'gmpay':
            $money = $amount !== null ? (float)$amount : (float)$o['amount'];
            if ($o['gw'] === 'gmpay') {
                $n = ['pid' => $keys['gm_pid'], 'trade_id' => $o['trade_id'], 'order_id' => $no, 'amount' => $money, 'actual_amount' => 1.0137,
                    'receive_address' => 'TMockReceiveAddress', 'token' => 'USDT', 'block_transaction_id' => 'ab' . md5($no), 'status' => 2];
                $n['signature'] = hash_hmac('sha256', canonical($n, ['signature']), $keys['gm_secret']);
            } else {
                $n = ['trade_id' => $o['trade_id'], 'order_id' => $no, 'amount' => $money, 'actual_amount' => 1.01,
                    'token' => 'TMockReceiveAddress', 'block_transaction_id' => 'ab' . md5($no), 'status' => 2];
                $n['signature'] = md5(canonical($n, ['signature']) . $keys['usdt_token']);
            }
            if ($tamper) {
                $n['signature'] = md5('forged');
            }
            json_reply(deliver('POST', $o['notify_url'], json_encode($n), ['Content-Type' => 'application/json']));
            return;
    }
}

if ($path === '/_mock/orders') {
    json_reply(orders());
    return;
}

reply(404, 'mock: no route ' . $path);
