<?php
declare(strict_types=1);

/**
 * 支付网关模拟器（只用于本地端到端测试，配合 tests/pay/e2e.php）。
 *
 *   php -S 127.0.0.1:9100 tests/pay/MockGateway.php
 *
 * 模拟易支付、支付宝开放平台、微信支付 APIv3 三类网关：
 * 校验站点发来的下单请求签名、返回带签名的应答、按各平台格式发送签名正确（或故意篡改）的异步通知，
 * 并提供易支付、支付宝、微信的查单接口（主动查单测试用）；
 * 还模拟币安现货 API 的充值地址、充值记录、Pay 交易记录（币安个人账户收款测试用），到账记录由 /_mock/binance/* 写入。
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

/** 改订单记录里的几个字段（付款状态、查单次数），不覆盖下单时记下的内容 */
function patch(string $tradeNo, array $fields): void
{
    $all = orders();
    if (isset($all[$tradeNo])) {
        $all[$tradeNo] = array_merge($all[$tradeNo], $fields);
        file_put_contents(MOCK_DIR . '/orders.json', json_encode($all, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES));
    }
}

/** 记一次查单，返回订单记录（没有这笔单返回 null） */
function queried(string $tradeNo): ?array
{
    $o = orders()[$tradeNo] ?? null;
    if ($o) {
        patch($tradeNo, ['queries' => (int)($o['queries'] ?? 0) + 1]);
    }
    return $o;
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

// 易支付查单：api.php?act=order&pid=&key=&out_trade_no=（彩虹易支付原版的返回格式）
if ($path === '/epay/api.php' && ($_GET['act'] ?? '') === 'order') {
    if ((string)($_GET['pid'] ?? '') !== (string)$keys['epay_pid']) {
        json_reply(['code' => -3, 'msg' => 'PID不存在']);
        return;
    }
    if ((string)($_GET['key'] ?? '') !== $keys['epay_key']) {
        json_reply(['code' => -2, 'msg' => 'KEY校验失败']);
        return;
    }
    $no = (string)($_GET['out_trade_no'] ?? '');
    $o = queried($no);
    if (!$o || $o['gw'] !== 'epay') {
        json_reply(['code' => -1, 'msg' => '订单号不存在']);
        return;
    }
    json_reply(['code' => 1, 'msg' => '查询订单号成功！', 'trade_no' => 'EP' . $no, 'out_trade_no' => $no, 'type' => $o['type'],
        'pid' => (int)$keys['epay_pid'], 'addtime' => date('Y-m-d H:i:s'), 'endtime' => empty($o['paid']) ? null : date('Y-m-d H:i:s'),
        'name' => $o['name'], 'money' => empty($o['paid']) ? $o['money'] : $o['paid_amount'], 'status' => empty($o['paid']) ? 0 : 1]);
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
    if ($p['method'] === 'alipay.trade.query') {
        $no = (string)($biz['out_trade_no'] ?? '');
        $o = queried($no);
        $node = !$o || $o['gw'] !== 'alipay'
            ? ['code' => '40004', 'msg' => 'Business Failed', 'sub_code' => 'ACQ.TRADE_NOT_EXIST', 'sub_msg' => '交易不存在']
            : ['code' => '10000', 'msg' => 'Success', 'trade_no' => '2026' . $no, 'out_trade_no' => $no, 'buyer_logon_id' => 'tes***@example.com',
                'trade_status' => empty($o['paid']) ? 'WAIT_BUYER_PAY' : 'TRADE_SUCCESS',
                'total_amount' => empty($o['paid']) ? $o['total_amount'] : $o['paid_amount'], 'buyer_pay_amount' => '0.00'];
        $json = json_encode($node, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        reply(200, '{"alipay_trade_query_response":' . $json . ',"sign":"' . rsa_sign($json, $keys['alipay_platform_private']) . '"}', ['Content-Type' => 'text/html;charset=utf-8']);
        return;
    }
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
// 商户订单号查单：GET /v3/pay/transactions/out-trade-no/{单号}?mchid=，签名串里是带查询串的路径、主体为空
if (preg_match('#^/wechat/v3/pay/transactions/out-trade-no/(\d+)$#', $path, $qm)) {
    $auth = (string)($_SERVER['HTTP_AUTHORIZATION'] ?? '');
    preg_match_all('/(\w+)="([^"]*)"/', $auth, $m);
    $a = array_combine($m[1], $m[2]);
    $apiPath = substr((string)$_SERVER['REQUEST_URI'], strlen('/wechat'));
    $message = "GET\n{$apiPath}\n" . ($a['timestamp'] ?? '') . "\n" . ($a['nonce_str'] ?? '') . "\n\n";
    if ($_SERVER['REQUEST_METHOD'] !== 'GET' || !rsa_verify($message, $a['signature'] ?? '', $keys['wechat_merchant_public'])
        || ($a['serial_no'] ?? '') !== $keys['wechat_merchant_serial'] || ($a['mchid'] ?? '') !== ($_GET['mchid'] ?? '')) {
        json_reply(['code' => 'SIGN_ERROR', 'message' => '签名错误'], 401);
        return;
    }
    $o = queried($qm[1]);
    if (!$o || $o['gw'] !== 'wechat') {
        json_reply(['code' => 'ORDER_NOT_EXIST', 'message' => '订单不存在'], 404);
        return;
    }
    $total = empty($o['paid']) ? $o['total'] : (int)round((float)$o['paid_amount'] * 100);
    $body = json_encode(['appid' => $o['appid'], 'mchid' => $o['mchid'], 'out_trade_no' => $qm[1], 'transaction_id' => '4200' . $qm[1],
        'trade_type' => 'NATIVE', 'trade_state' => empty($o['paid']) ? 'NOTPAY' : 'SUCCESS', 'trade_state_desc' => empty($o['paid']) ? '未支付' : '支付成功',
        'amount' => ['total' => $total, 'payer_total' => $total, 'currency' => 'CNY', 'payer_currency' => 'CNY']], JSON_UNESCAPED_UNICODE);
    $ts = (string)time();
    $nonce = bin2hex(random_bytes(8));
    reply(200, $body, ['Content-Type' => 'application/json', 'Wechatpay-Timestamp' => $ts, 'Wechatpay-Nonce' => $nonce,
        'Wechatpay-Serial' => $keys['wechat_platform_serial'], 'Wechatpay-Signature' => rsa_sign("{$ts}\n{$nonce}\n{$body}\n", $keys['wechat_platform_private'])]);
    return;
}

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

// ---------------------------------------------------------------- 币安现货 API（只读）
function binance(): array
{
    $b = json_decode((string)@file_get_contents(MOCK_DIR . '/binance.json'), true) ?: [];
    return $b + ['deposits' => [], 'pay' => [], 'calls' => []];
}

function binance_save(array $b): void
{
    file_put_contents(MOCK_DIR . '/binance.json', json_encode($b, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES));
}

if (str_starts_with($path, '/binance/')) {
    //签名：signature 必须是查询串最后一个参数，对它前面的整段做 HMAC-SHA256
    $qs = (string)($_SERVER['QUERY_STRING'] ?? '');
    $cut = strrpos($qs, '&signature=');
    if (($_SERVER['HTTP_X_MBX_APIKEY'] ?? '') !== $keys['binance_key']) {
        json_reply(['code' => -2015, 'msg' => 'Invalid API-key, IP, or permissions for action.'], 401);
        return;
    }
    if ($cut === false || !hash_equals(hash_hmac('sha256', substr($qs, 0, $cut), $keys['binance_secret']), substr($qs, $cut + 11))
        || abs((int)($_GET['timestamp'] ?? 0) - (int)floor(microtime(true) * 1000)) > (int)($_GET['recvWindow'] ?? 5000)) {
        json_reply(['code' => -1022, 'msg' => 'Signature for this request is not valid.'], 400);
        return;
    }

    $api = substr($path, strlen('/binance'));
    $b = binance();
    $b['calls'][$api] = (int)($b['calls'][$api] ?? 0) + 1;
    binance_save($b);
    $since = (int)($_GET['startTime'] ?? 0);

    if ($api === '/sapi/v1/capital/deposit/address') {
        json_reply(['address' => 'Mock' . ($_GET['network'] ?? '') . 'DepositAddress', 'coin' => $_GET['coin'] ?? '', 'tag' => '', 'url' => '']);
    } elseif ($api === '/sapi/v1/capital/deposit/hisrec') {
        json_reply(array_values(array_filter($b['deposits'], fn($r) => $r['coin'] === ($_GET['coin'] ?? $r['coin']) && $r['insertTime'] >= $since)));
    } elseif ($api === '/sapi/v1/pay/transactions') {
        json_reply(['code' => '000000', 'message' => 'success', 'data' => array_values(array_filter($b['pay'], fn($r) => $r['transactionTime'] >= $since)), 'success' => true]);
    } else {
        json_reply(['code' => -1000, 'msg' => 'mock: unknown endpoint'], 404);
    }
    return;
}

// /_mock/binance/deposit?amount=&network=TRX[&status=1][&time=毫秒]：账户收到一笔链上充值
// /_mock/binance/pay?amount=[&currency=USDT][&type=C2C][&time=毫秒]：账户收到一笔币安 Pay 转账（amount 为负表示转出）
// /_mock/binance/calls：各接口被调用的次数
if (str_starts_with($path, '/_mock/binance/')) {
    $b = binance();
    $time = (int)($_GET['time'] ?? floor(microtime(true) * 1000));
    if ($path === '/_mock/binance/deposit') {
        $b['deposits'][] = ['id' => (string)random_int(1000000000000000, 9000000000000000), 'amount' => sprintf('%.8F', (float)$_GET['amount']), 'coin' => 'USDT',
            'network' => $_GET['network'] ?? 'TRX', 'status' => (int)($_GET['status'] ?? 1), 'address' => 'Mock' . ($_GET['network'] ?? 'TRX') . 'DepositAddress',
            'addressTag' => '', 'txId' => bin2hex(random_bytes(32)), 'insertTime' => $time, 'completeTime' => $time, 'transferType' => 0,
            'confirmTimes' => '1/1', 'unlockConfirm' => 0, 'walletType' => 0, 'travelRuleStatus' => 0];
    } elseif ($path === '/_mock/binance/pay') {
        $amount = sprintf('%.8F', (float)$_GET['amount']);
        $b['pay'][] = ['orderType' => $_GET['type'] ?? 'C2C', 'transactionId' => 'M' . random_int(1000000000000000, 9000000000000000), 'transactionTime' => $time,
            'amount' => $amount, 'currency' => $_GET['currency'] ?? 'USDT', 'walletType' => 1, 'walletTypes' => [1],
            'fundsDetail' => [['currency' => $_GET['currency'] ?? 'USDT', 'amount' => $amount]],
            'payerInfo' => ['name' => 'Buyer', 'type' => 'USER', 'binanceId' => 10000001], 'receiverInfo' => ['name' => 'Shop', 'type' => 'USER', 'binanceId' => 10000002]];
    } elseif ($path === '/_mock/binance/calls') {
        json_reply($b['calls']);
        return;
    }
    binance_save($b);
    json_reply(['ok' => true]);
    return;
}

// ---------------------------------------------------------------- 测试控制：发送异步通知
// /_mock/notify?order=单号[&tamper=1][&amount=x][&finished=1][&to=回调地址]
// to：把这份签名正确的通知投到别的回调地址，用来测「拿 A 单的合法通知去刷 B 单」
if ($path === '/_mock/notify') {
    $no = (string)($_GET['order'] ?? '');
    $o = orders()[$no] ?? null;
    if (!$o) {
        json_reply(['error' => 'unknown order'], 404);
        return;
    }
    $tamper = !empty($_GET['tamper']);
    $amount = $_GET['amount'] ?? null;
    $target = (string)($_GET['to'] ?? $o['notify_url']);

    switch ($o['gw']) {
        case 'epay':
            $n = ['pid' => $keys['epay_pid'], 'trade_no' => 'EP' . $no, 'out_trade_no' => $no, 'type' => $o['type'], 'name' => $o['name'],
                'money' => $amount ?? $o['money'], 'trade_status' => 'TRADE_SUCCESS', 'param' => ''];
            $n['sign'] = md5(canonical($n, ['sign', 'sign_type']) . $keys['epay_key']);
            $n['sign_type'] = 'MD5';
            if ($tamper) {
                $n['money'] = '0.01';
            }
            json_reply(deliver('GET', $target, $n));
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
            json_reply(deliver('POST', $target, $n, ['Content-Type' => 'application/x-www-form-urlencoded; charset=utf-8']));
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
            json_reply(deliver('POST', $target, $body, ['Content-Type' => 'application/json', 'Wechatpay-Timestamp' => $ts,
                'Wechatpay-Nonce' => $hn, 'Wechatpay-Signature' => $sig, 'Wechatpay-Serial' => $keys['wechat_platform_serial']]));
            return;
    }
}

// /_mock/pay?order=单号[&amount=实付金额]：在平台侧把这笔单标成已付款，但不发通知（模拟通知丢失）
if ($path === '/_mock/pay') {
    $no = (string)($_GET['order'] ?? '');
    $o = orders()[$no] ?? null;
    if (!$o) {
        json_reply(['error' => 'unknown order'], 404);
        return;
    }
    $amount = $_GET['amount'] ?? match ($o['gw']) {
        'epay' => $o['money'],
        'alipay' => $o['total_amount'],
        'wechat' => number_format($o['total'] / 100, 2, '.', ''),
        default => '',
    };
    patch($no, ['paid' => true, 'paid_amount' => (string)$amount]);
    json_reply(['ok' => true, 'paid_amount' => (string)$amount]);
    return;
}

if ($path === '/_mock/orders') {
    json_reply(orders());
    return;
}

reply(404, 'mock: no route ' . $path);
