<?php
declare(strict_types=1);

/**
 * 支付插件端到端测试：真实下单 → 网关模拟器收单 → 异步通知 → 订单已付款且已发货。
 *
 * 在站点容器里运行（需要能连本站数据库，站点监听 127.0.0.1:80）：
 *
 *   docker cp tests faka-local:/tmp/acg-tests
 *   docker exec faka-local php /tmp/acg-tests/pay/e2e.php [--item=9] [--keep]
 *
 * 测试期间会临时关闭下单验证码；结束后删除测试订单（归还卡密）、测试支付接口与配置，并恢复设置。
 * --keep     保留测试订单与支付接口，方便到后台查看
 * --cleanup  只清理此前 --keep 留下的测试数据（按 E2E 名称前缀和 e2e-test 联系方式识别）
 */

const SITE = 'http://127.0.0.1';
const MOCK = 'http://127.0.0.1:9100';
const MOCK_DIR = '/tmp/acg-pay-mock';
const BASE = '/var/www/html';
const CONTACT = 'e2e-test';
const LABEL = 'E2E ';

$opts = getopt('', ['item::', 'keep', 'cleanup']);
$itemId = (int)($opts['item'] ?? 9);
$keep = isset($opts['keep']);

// ------------------------------------------------------------ 基础设施
$db = require BASE . '/config/database.php';
$pdo = new PDO("mysql:host={$db['host']};port={$db['port']};dbname={$db['database']};charset=utf8mb4", $db['username'], $db['password'],
    [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION, PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC]);
$t = fn(string $name) => $db['prefix'] . $name;

$results = [];
function check(string $name, bool $ok, string $detail = ''): void
{
    global $results;
    $results[] = [$name, $ok, $detail];
    printf("  %s %s%s\n", $ok ? '✔' : '✘', $name, $ok || $detail === '' ? '' : "  —  {$detail}");
}

function http(string $method, string $url, array|string|null $body = null, array $headers = []): array
{
    $ch = curl_init($url);
    $hdr = ['User-Agent: Mozilla/5.0 (acg-faka e2e)'];
    foreach ($headers as $k => $v) {
        $hdr[] = "{$k}: {$v}";
    }
    if ($method === 'POST') {
        curl_setopt($ch, CURLOPT_POST, true);
        curl_setopt($ch, CURLOPT_POSTFIELDS, is_array($body) ? http_build_query($body) : (string)$body);
    }
    curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true, CURLOPT_HTTPHEADER => $hdr, CURLOPT_TIMEOUT => 30]);
    $resp = (string)curl_exec($ch);
    $code = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['code' => $code, 'body' => $resp];
}

function config_set(PDO $pdo, string $table, string $key, string $value): void
{
    $pdo->prepare("UPDATE {$table} SET value = ? WHERE `key` = ?")->execute([$value, $key]);
    @unlink(BASE . '/runtime/config');
}

function keypair(): array
{
    $res = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
    openssl_pkey_export($res, $private);
    $public = openssl_pkey_get_details($res)['key'];
    return [$private, $public];
}

/** 删除测试订单（先把卡密归还）、测试支付接口与配置；按名称前缀与联系方式识别，不碰站长自己的数据 */
function cleanup(PDO $pdo, callable $t): void
{
    $ids = $pdo->query("SELECT id FROM {$t('order')} WHERE contact = " . $pdo->quote(CONTACT))->fetchAll(PDO::FETCH_COLUMN);
    if ($ids) {
        $idList = implode(',', array_map('intval', $ids));
        $pdo->exec("UPDATE {$t('card')} SET status = 0, order_id = NULL, purchase_time = NULL WHERE order_id IN ({$idList})");
        $pdo->exec("DELETE FROM {$t('order')} WHERE id IN ({$idList})");
    }
    $pdo->exec("DELETE FROM {$t('pay')} WHERE name LIKE " . $pdo->quote(LABEL . '%'));
    $pdo->exec("DELETE FROM {$t('pay_config')} WHERE name LIKE " . $pdo->quote(LABEL . '%'));
    printf("已清理 %d 笔测试订单及测试支付接口/配置\n", count($ids));
}

if (isset($opts['cleanup'])) {
    cleanup($pdo, $t);
    exit(0);
}

/** 支付宝密钥工具导出的是去掉头尾的单行 base64，这里同样喂单行格式，顺便测 PEM 兼容 */
function bare(string $pem): string
{
    return preg_replace('/-----[^-]+-----|\s+/', '', $pem);
}

// ------------------------------------------------------------ 密钥与网关模拟器
@mkdir(MOCK_DIR, 0777, true);
@unlink(MOCK_DIR . '/orders.json');
[$aliAppPriv, $aliAppPub] = keypair();
[$aliPlatPriv, $aliPlatPub] = keypair();
[$wxMchPriv, $wxMchPub] = keypair();
[$wxPlatPriv, $wxPlatPub] = keypair();
$keys = [
    'epay_pid' => '1001', 'epay_key' => 'EpayMockKey123456',
    'alipay_app_public' => $aliAppPub, 'alipay_platform_private' => $aliPlatPriv,
    'wechat_merchant_public' => $wxMchPub, 'wechat_merchant_serial' => '5157F09EFDC096DE15EBE81A47057A72',
    'wechat_platform_private' => $wxPlatPriv, 'wechat_platform_serial' => 'PUB_KEY_ID_0114232134912410000000000000',
    'wechat_apiv3_key' => 'MockApiV3Key0123456789abcdefABCD',
    'usdt_token' => 'epusdt_password_mock', 'gm_pid' => '1000', 'gm_secret' => 'gm_secret_mock',
];
file_put_contents(MOCK_DIR . '/keys.json', json_encode($keys));

$mock = proc_open(['php', '-S', '127.0.0.1:9100', __DIR__ . '/MockGateway.php'], [1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes);
usleep(400000);

$configs = [
    'Yipay.submit' => ['Yipay', ['url' => MOCK . '/epay/submit.php', 'pid' => '1001', 'key' => 'EpayMockKey123456', 'mode' => 'submit']],
    'Yipay.mapi' => ['Yipay', ['url' => MOCK . '/epay', 'pid' => '1001', 'key' => 'EpayMockKey123456', 'mode' => 'mapi']],
    'Alipay' => ['AlipayOpen', ['app_id' => '2021000000000001', 'private_key' => bare($aliAppPriv), 'alipay_public_key' => bare($aliPlatPub), 'gateway' => MOCK . '/alipay/gateway.do']],
    'Alipay.wrongkey' => ['AlipayOpen', ['app_id' => '2021000000000001', 'private_key' => $aliAppPriv, 'alipay_public_key' => keypair()[1], 'gateway' => MOCK . '/alipay/gateway.do']],
    'Wechat' => ['WechatPayV3', ['mchid' => '1900000001', 'appid' => 'wx0000000000000001', 'apiv3_key' => $keys['wechat_apiv3_key'],
        'serial_no' => $keys['wechat_merchant_serial'], 'private_key' => $wxMchPriv, 'wechatpay_public_key' => $wxPlatPub,
        'wechatpay_serial' => $keys['wechat_platform_serial'], 'api_base' => MOCK . '/wechat']],
    'Usdt' => ['UsdtGateway', ['protocol' => 'epusdt', 'url' => MOCK . '/usdt', 'token' => 'epusdt_password_mock', 'timeout' => '1200']],
    'GmPay' => ['UsdtGateway', ['protocol' => 'gmpay', 'url' => MOCK . '/usdt', 'token' => 'gm_secret_mock', 'pid' => '1000']],
];

// [名称, 配置, code, 呈现方式, 期望内容, 额外检查]
$scenarios = [
    ['易支付·跳转收银台', 'Yipay.submit', 'alipay', 'submit', MOCK . '/epay/submit.php', ['tamper']],
    ['易支付·接口下单', 'Yipay.mapi', 'wxpay', 'qrcode', 'weixin://wxpay/bizpayurl?pr=mockepay', []],
    ['支付宝·当面付', 'Alipay', 'face', 'qrcode', 'https://qr.alipay.com/mock', ['amount']],
    ['支付宝·电脑网站', 'Alipay', 'page', 'submit', MOCK . '/alipay/gateway.do?charset=utf-8', ['finished']],
    ['支付宝·手机网站', 'Alipay', 'wap', 'submit', MOCK . '/alipay/gateway.do?charset=utf-8', []],
    ['微信·Native', 'Wechat', 'native', 'qrcode', 'weixin://wxpay/bizpayurl?pr=mock', ['tamper']],
    ['微信·H5', 'Wechat', 'h5', 'jump', 'checkmweb?prepay_id=wxmock', []],
    ['收U·BEpusdt', 'Usdt', 'usdt.trc20', 'redirect', MOCK . '/usdt/pay/checkout-counter/', ['tamper', 'duplicate']],
    ['收U·GM Pay', 'GmPay', 'cashier', 'redirect', MOCK . '/usdt/pay/checkout-counter/', []],
];

$tradeVerification = $pdo->query("SELECT value FROM {$t('config')} WHERE `key` = 'trade_verification'")->fetchColumn();

try {
    config_set($pdo, $t('config'), 'trade_verification', '0');

    $configIds = [];
    foreach ($configs as $label => [$handle, $config]) {
        $pdo->prepare("INSERT INTO {$t('pay_config')} (handle, name, config, sort, create_time) VALUES (?, ?, ?, 0, NOW())")
            ->execute([$handle, LABEL . $label, json_encode($config, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES)]);
        $configIds[$label] = (int)$pdo->lastInsertId();
    }

    $newPay = function (string $handle, string $code, int $configId) use ($pdo, $t): int {
        $pdo->prepare("INSERT INTO {$t('pay')} (name, icon, code, commodity, recharge, create_time, handle, pay_config_id, sort, equipment, cost, cost_type, archived)
            VALUES (?, '/assets/user/images/cash/alipay.png', ?, 1, 0, NOW(), ?, ?, 0, 0, 0, 0, 0)")
            ->execute([LABEL . substr($code, 0, 10), $code, $handle, $configId]);
        return (int)$pdo->lastInsertId();
    };

    $order = function (int $payId) use ($itemId): array {
        $res = http('POST', SITE . '/user/api/order/trade', ['item_id' => $itemId, 'num' => 1, 'contact' => CONTACT, 'pay_id' => $payId]);
        return json_decode($res['body'], true) ?: [];
    };

    $orderRow = fn(string $no) => $pdo->query("SELECT status, amount, gateway_amount, secret FROM {$t('order')} WHERE trade_no = " . $pdo->quote($no))->fetch();
    $notify = fn(string $no, array $q = []) => json_decode(http('GET', MOCK . '/_mock/notify?' . http_build_query(['order' => $no] + $q))['body'], true) ?: [];

    foreach ($scenarios as [$name, $configLabel, $code, $mode, $expect, $extras]) {
        echo "\n▶ {$name}\n";
        [$handle] = $configs[$configLabel];
        $payId = $newPay($handle, $code, $configIds[$configLabel]);

        $res = $order($payId);
        if (($res['code'] ?? 0) !== 200) {
            check('下单', false, $res['msg'] ?? json_encode($res, JSON_UNESCAPED_UNICODE));
            continue;
        }
        $no = $res['data']['tradeNo'];
        $url = (string)$res['data']['url'];
        check("下单成功 #{$no}，¥{$res['data']['amount']}", true);

        // 走一遍买家会看到的支付页
        if ($mode === 'redirect') {
            check('跳转到网关收银台', str_starts_with($url, $expect), $url);
        } else {
            $page = http('GET', SITE . $url);
            if ($mode === 'submit') {
                preg_match('/<form action="([^"]+)"/', $page['body'], $fm);
                preg_match_all('/name="([^"]+)" value="([^"]*)"/', $page['body'], $fields, PREG_SET_ORDER);
                $form = [];
                foreach ($fields as [, $k, $v]) {
                    $form[$k] = html_entity_decode($v, ENT_QUOTES);
                }
                $action = html_entity_decode($fm[1] ?? '', ENT_QUOTES);
                check('表单提交到网关', $action === $expect, $action);
                $cashier = http('POST', $action, $form);
                check('网关验签通过并出收银台', $cashier['code'] === 200 && str_contains($cashier['body'], 'MOCK'), substr($cashier['body'], 0, 120));
            } else {
                preg_match('/data-mode="([^"]+)"/', $page['body'], $pm);
                preg_match('/data-content="([^"]+)"/', $page['body'], $cm);
                $content = html_entity_decode($cm[1] ?? '', ENT_QUOTES);
                check("本站收银台（{$mode}）", ($pm[1] ?? '') === $mode && str_contains($content, $expect), $content ?: substr($page['body'], 0, 200));
                if ($mode === 'jump') {
                    check('H5 链接带 redirect_url', str_contains($content, 'redirect_url='), $content);
                }
            }
        }

        if (in_array('tamper', $extras, true)) {
            $r = $notify($no, ['tamper' => 1]);
            check('篡改签名的通知被拒绝', ($orderRow($no)['status'] ?? null) == 0 && !in_array(strtolower(trim($r['body'] ?? '')), ['success', 'ok'], true),
                "HTTP {$r['http_code']} " . substr($r['body'] ?? '', 0, 80));
            if ($handle === 'WechatPayV3') {
                check('微信验签失败返回 HTTP 500（让微信重试）', ($r['http_code'] ?? 0) === 500, (string)($r['http_code'] ?? ''));
            }
        }
        if (in_array('amount', $extras, true)) {
            $r = $notify($no, ['amount' => '0.01']);
            check('金额不符的通知被拒绝（签名正确）', ($orderRow($no)['status'] ?? null) == 0, substr($r['body'] ?? '', 0, 80));
        }

        $r = $notify($no, in_array('finished', $extras, true) ? ['finished' => 1] : []);
        $ack = trim($r['body'] ?? '');
        $row = $orderRow($no);
        check('回调应答 ' . ($ack === '' ? '(空)' : $ack), in_array($ack, ['success', '{"code":"SUCCESS","message":"成功"}'], true), "HTTP {$r['http_code']}");
        check('订单已付款', ($row['status'] ?? null) == 1, json_encode($row, JSON_UNESCAPED_UNICODE));
        check('已自动发货', !empty($row['secret']), (string)($row['secret'] ?? ''));

        if (in_array('duplicate', $extras, true)) {
            $r = $notify($no);
            check('重复通知不会重复处理', ($orderRow($no)['status'] ?? null) == 1 && trim($r['body'] ?? '') !== 'success', substr($r['body'] ?? '', 0, 80));
        }
    }

    echo "\n▶ 支付宝应答验签（配置了错误的支付宝公钥）\n";
    $payId = $newPay('AlipayOpen', 'face', $configIds['Alipay.wrongkey']);
    $res = $order($payId);
    check('伪造/不匹配的网关应答被拒，不展示二维码', ($res['code'] ?? 0) !== 200 && str_contains((string)($res['msg'] ?? ''), '验签不通过'), (string)($res['msg'] ?? ''));
} finally {
    if (!$keep) {
        cleanup($pdo, $t);
    }
    config_set($pdo, $t('config'), 'trade_verification', (string)$tradeVerification);
    proc_terminate($mock);
}

$failed = array_filter($results, fn($r) => !$r[1]);
printf("\n%d 项检查，%d 通过，%d 失败%s\n", count($results), count($results) - count($failed), count($failed), $keep ? '（--keep：测试数据已保留）' : '（测试数据已清理）');
exit($failed ? 1 : 0);
