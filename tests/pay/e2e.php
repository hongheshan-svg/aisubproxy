<?php
declare(strict_types=1);

/**
 * 支付插件端到端测试：真实下单 / 余额充值 → 网关模拟器收单 → 异步通知 → 订单已付款并发货 / 余额到账；
 * 以及通知丢失时的主动查单（收银台页面触发、PayKit/cron.php 定时任务触发）。
 *
 * 在站点容器里运行（需要能连本站数据库，站点监听 127.0.0.1:80）：
 *
 *   docker cp tests faka-local:/tmp/acg-tests
 *   docker exec faka-local php /tmp/acg-tests/pay/e2e.php [--item=9] [--keep]
 *
 * 充值部分会建一个临时会员 e2e_recharge，并直接签发登录会话（不走登录页，避开验证码）。
 * 测试期间会临时关闭下单验证码；结束后删除测试订单（归还卡密）、临时会员及其充值单/账单/会话、测试支付接口与配置，并恢复设置。
 * 每次运行前会先清掉上次 --keep 留下的测试数据。
 * --keep     保留测试数据，方便到后台查看
 * --cleanup  只清理此前 --keep 留下的测试数据（按 E2E 名称前缀、e2e-test 联系方式和 e2e_recharge 会员名识别）
 */

const SITE = 'http://127.0.0.1';
const MOCK = 'http://127.0.0.1:9100';
const MOCK_DIR = '/tmp/acg-pay-mock';
const BASE = '/var/www/html';
const CONTACT = 'e2e-test';
const LABEL = 'E2E ';
const TEST_USER = 'e2e_recharge';
const RECHARGE_AMOUNT = '12.34';
const ORDER_CALLBACK = '/user/api/order/callback.';
const RECHARGE_CALLBACK = '/user/api/rechargeNotification/callback.';

date_default_timezone_set('Asia/Shanghai'); //与站点一致：要按站点时区改订单的 create_time

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

/** 金额统一成两位小数字符串再比，避免浮点误差 */
function cents(mixed $value): string
{
    return number_format((float)$value, 2, '.', '');
}

/**
 * 删除测试数据，按名称前缀、联系方式和临时会员名识别，不碰站长自己的数据：
 * 测试订单（先归还卡密）、临时会员及其充值单/账单/会话、测试支付接口与配置。
 * @return array{0:int,1:int} [订单数, 会员数]
 */
function cleanup(PDO $pdo, callable $t): array
{
    $ids = $pdo->query("SELECT id FROM {$t('order')} WHERE contact = " . $pdo->quote(CONTACT))->fetchAll(PDO::FETCH_COLUMN);
    if ($ids) {
        $idList = implode(',', array_map('intval', $ids));
        $pdo->exec("UPDATE {$t('card')} SET status = 0, order_id = NULL, purchase_time = NULL WHERE order_id IN ({$idList})");
        $pdo->exec("DELETE FROM {$t('order')} WHERE id IN ({$idList})");
    }
    $users = $pdo->query("SELECT id FROM {$t('user')} WHERE username = " . $pdo->quote(TEST_USER))->fetchAll(PDO::FETCH_COLUMN);
    if ($users) {
        $userList = implode(',', array_map('intval', $users));
        $pdo->exec("DELETE FROM {$t('user_recharge')} WHERE user_id IN ({$userList})");
        $pdo->exec("DELETE FROM {$t('bill')} WHERE owner IN ({$userList})");
        $pdo->exec("DELETE FROM {$t('user_session')} WHERE user_id IN ({$userList})");
        $pdo->exec("DELETE FROM {$t('user')} WHERE id IN ({$userList})");
    }
    $pdo->exec("DELETE FROM {$t('pay')} WHERE name LIKE " . $pdo->quote(LABEL . '%'));
    $pdo->exec("DELETE FROM {$t('pay_config')} WHERE name LIKE " . $pdo->quote(LABEL . '%'));
    return [count($ids), count($users)];
}

if (isset($opts['cleanup'])) {
    [$orders, $users] = cleanup($pdo, $t);
    printf("已清理 %d 笔测试订单、%d 个临时会员及测试支付接口/配置\n", $orders, $users);
    exit(0);
}

/** 支付宝密钥工具导出的是去掉头尾的单行 base64，这里同样喂单行格式，顺便测 PEM 兼容 */
function bare(string $pem): string
{
    return preg_replace('/-----[^-]+-----|\s+/', '', $pem);
}

/**
 * 建临时会员并直接签发登录会话，格式与 App\Service\UserSessionManager::issue 一致：
 * cookie = base64(JWT)，JWT 头带 uid、用会员密码哈希做 HS256 密钥，会话表只存 sid 的 SHA-256。
 * @return array{0:int,1:string} [会员ID, USER_SESSION cookie]
 */
function test_member(PDO $pdo, callable $t): array
{
    $password = bin2hex(random_bytes(32));
    $pdo->prepare("INSERT INTO {$t('user')} (username, password, salt, app_key, avatar, balance, create_time, status) VALUES (?, ?, ?, ?, '/favicon.ico', 0, NOW(), 1)")
        ->execute([TEST_USER, $password, bin2hex(random_bytes(16)), strtoupper(bin2hex(random_bytes(8)))]);
    $uid = (int)$pdo->lastInsertId();

    $sid = rtrim(strtr(base64_encode(random_bytes(32)), '+/', '-_'), '=');
    //过期时间放宽到一天：CLI 与站点的时区可能不同，会话表存的是本地时间字符串
    $pdo->prepare("INSERT INTO {$t('user_session')} (user_id, session_hash, device_type, device_name, user_agent, login_ip, last_ip, created_time, last_seen_time, expires_time)
        VALUES (?, ?, 'desktop', 'e2e', 'acg-faka e2e', '127.0.0.1', '127.0.0.1', NOW(), NOW(), DATE_ADD(NOW(), INTERVAL 1 DAY))")
        ->execute([$uid, hash('sha256', $sid)]);

    $b64 = fn(string $s) => rtrim(strtr(base64_encode($s), '+/', '-_'), '=');
    $head = $b64(json_encode(['typ' => 'JWT', 'alg' => 'HS256', 'uid' => $uid]));
    $body = $b64(json_encode(['uid' => $uid, 'sid' => $sid, 'iat' => time(), 'exp' => time() + 86400]));
    $sig = $b64(hash_hmac('sha256', "{$head}.{$body}", $password, true));
    return [$uid, base64_encode("{$head}.{$body}.{$sig}")];
}

// ------------------------------------------------------------ 密钥与网关模拟器
@mkdir(MOCK_DIR, 0777, true);
@unlink(MOCK_DIR . '/orders.json');
@unlink(MOCK_DIR . '/cron-state.json');
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
    cleanup($pdo, $t);
    config_set($pdo, $t('config'), 'trade_verification', '0');

    $configIds = [];
    foreach ($configs as $label => [$handle, $config]) {
        $pdo->prepare("INSERT INTO {$t('pay_config')} (handle, name, config, sort, create_time) VALUES (?, ?, ?, 0, NOW())")
            ->execute([$handle, LABEL . $label, json_encode($config, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES)]);
        $configIds[$label] = (int)$pdo->lastInsertId();
    }

    //商品下单用的接口只开「商品购买」，充值用的只开「余额充值」；$cost 是固定手续费
    $newPay = function (string $handle, string $code, int $configId, bool $recharge = false, string $cost = '0') use ($pdo, $t): int {
        $pdo->prepare("INSERT INTO {$t('pay')} (name, icon, code, commodity, recharge, create_time, handle, pay_config_id, sort, equipment, cost, cost_type, archived)
            VALUES (?, '/assets/user/images/cash/alipay.png', ?, ?, ?, NOW(), ?, ?, 0, 0, ?, 0, 0)")
            ->execute([LABEL . substr($code, 0, 10), $code, $recharge ? 0 : 1, $recharge ? 1 : 0, $handle, $configId, $cost]);
        return (int)$pdo->lastInsertId();
    };

    $order = function (int $payId) use ($itemId): array {
        $res = http('POST', SITE . '/user/api/order/trade', ['item_id' => $itemId, 'num' => 1, 'contact' => CONTACT, 'pay_id' => $payId]);
        return json_decode($res['body'], true) ?: [];
    };

    $orderRow = fn(string $no) => $pdo->query("SELECT status, amount, gateway_amount, secret FROM {$t('order')} WHERE trade_no = " . $pdo->quote($no))->fetch();
    $rechargeRow = fn(string $no) => $pdo->query("SELECT status, amount, pay_cost, gateway_amount FROM {$t('user_recharge')} WHERE trade_no = " . $pdo->quote($no))->fetch();
    $notify = fn(string $no, array $q = []) => json_decode(http('GET', MOCK . '/_mock/notify?' . http_build_query(['order' => $no] + $q))['body'], true) ?: [];
    $acked = fn(array $r) => in_array(trim($r['body'] ?? ''), ['success', '{"code":"SUCCESS","message":"成功"}'], true);

    // 走一遍买家会看到的支付页；$returnPath 不为空时，顺带核对本站收银台「支付完成后返回」的地址。返回本站收银台页面（其他方式返回空串）
    $walk = function (string $mode, string $url, string $expect, ?string $returnPath = null): string {
        if ($mode === 'redirect') {
            check('跳转到网关收银台', str_starts_with($url, $expect), $url);
            return '';
        }
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
            return '';
        }
        preg_match('/data-mode="([^"]+)"/', $page['body'], $pm);
        preg_match('/data-content="([^"]+)"/', $page['body'], $cm);
        preg_match('/data-return="([^"]*)"/', $page['body'], $rm);
        $content = html_entity_decode($cm[1] ?? '', ENT_QUOTES);
        check("本站收银台（{$mode}）", ($pm[1] ?? '') === $mode && str_contains($content, $expect), $content ?: substr($page['body'], 0, 200));
        if ($mode === 'jump') {
            check('H5 链接带 redirect_url', str_contains($content, 'redirect_url='), $content);
        }
        if ($returnPath !== null) {
            $return = html_entity_decode($rm[1] ?? '', ENT_QUOTES);
            check("支付完成后返回 {$returnPath}", str_ends_with($return, $returnPath), $return);
        }
        return $page['body'];
    };

    // 核对网关实际收到的下单参数：金额等于订单的 CNY 快照，异步通知地址指向本单对应的回调
    $checkGateway = function (string $no, string $callbackPath, mixed $amount): void {
        $o = (json_decode(http('GET', MOCK . '/_mock/orders')['body'], true) ?: [])[$no] ?? null;
        if (!$o) {
            check('网关收到下单请求', false, '模拟器里没有这笔单');
            return;
        }
        $got = match ($o['gw']) {
            'epay' => $o['money'],
            'alipay' => $o['total_amount'],
            'wechat' => $o['total'] / 100,
            default => $o['amount'],
        };
        check('网关收到金额 ¥' . cents($amount), cents($got) === cents($amount), (string)$got);
        check('通知地址指向本单回调', str_ends_with((string)$o['notify_url'], $callbackPath . $no), (string)$o['notify_url']);
    };

    // ============================================================ 商品下单
    echo "\n━━ 商品下单 ━━\n";
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
        check("下单成功 #{$no}，¥{$res['data']['amount']}", true);

        $walk($mode, (string)$res['data']['url'], $expect);
        $checkGateway($no, ORDER_CALLBACK, $orderRow($no)['gateway_amount'] ?? null);

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
        check('回调应答 ' . ($ack === '' ? '(空)' : $ack), $acked($r), "HTTP {$r['http_code']}");
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

    // ============================================================ 余额充值
    echo "\n━━ 余额充值 ━━\n";
    [$memberId, $cookie] = test_member($pdo, $t);
    $member = fn() => $pdo->query("SELECT balance, recharge FROM {$t('user')} WHERE id = {$memberId}")->fetch();
    $lastBill = fn() => $pdo->query("SELECT amount, log FROM {$t('bill')} WHERE owner = {$memberId} ORDER BY id DESC LIMIT 1")->fetch() ?: [];
    $session = ['Cookie' => 'USER_SESSION=' . rawurlencode($cookie), 'Referer' => SITE . '/user/recharge/index'];
    $recharge = function (int $payId, string $amount) use ($session): array {
        $res = http('POST', SITE . '/user/api/recharge/trade', ['pay_id' => $payId, 'amount' => $amount], $session);
        return json_decode($res['body'], true) ?: [];
    };
    // 跟 $before 那次查询比，余额涨了多少
    $gained = fn(array $before) => cents((float)$member()['balance'] - (float)$before['balance']);

    $probe = $newPay('Yipay', 'alipay', $configIds['Yipay.submit'], true);
    $list = json_decode(http('POST', SITE . '/user/api/recharge/pay', [], $session)['body'], true) ?: [];
    check("临时会员 #{$memberId} 已登录，充值页能看到测试接口", ($list['code'] ?? 0) === 200 && in_array($probe, array_column($list['data'] ?? [], 'id')),
        json_encode($list, JSON_UNESCAPED_UNICODE));

    foreach ($scenarios as [$name, $configLabel, $code, $mode, $expect, $extras]) {
        echo "\n▶ 充值 · {$name}\n";
        [$handle] = $configs[$configLabel];
        $payId = $newPay($handle, $code, $configIds[$configLabel], true);

        $before = $member();
        $res = $recharge($payId, RECHARGE_AMOUNT);
        if (($res['code'] ?? 0) !== 200) {
            check('创建充值单', false, $res['msg'] ?? json_encode($res, JSON_UNESCAPED_UNICODE));
            continue;
        }
        $no = $res['data']['tradeNo'];
        check("充值单 #{$no}，¥{$res['data']['amount']}", cents($res['data']['amount']) === RECHARGE_AMOUNT);

        $walk($mode, (string)$res['data']['url'], $expect, '/user/recharge/index');
        $checkGateway($no, RECHARGE_CALLBACK, RECHARGE_AMOUNT);

        if (in_array('tamper', $extras, true)) {
            $r = $notify($no, ['tamper' => 1]);
            check('篡改签名的通知被拒绝，余额不变', ($rechargeRow($no)['status'] ?? null) == 0 && $gained($before) === '0.00' && !$acked($r),
                "HTTP {$r['http_code']} " . substr($r['body'] ?? '', 0, 80));
            if ($handle === 'WechatPayV3') {
                check('微信验签失败返回 HTTP 500（让微信重试）', ($r['http_code'] ?? 0) === 500, (string)($r['http_code'] ?? ''));
            }
        }
        if (in_array('amount', $extras, true)) {
            $r = $notify($no, ['amount' => '0.01']);
            check('金额不符的通知被拒绝（签名正确），余额不变', ($rechargeRow($no)['status'] ?? null) == 0 && $gained($before) === '0.00', substr($r['body'] ?? '', 0, 80));
        }

        $r = $notify($no, in_array('finished', $extras, true) ? ['finished' => 1] : []);
        $ack = trim($r['body'] ?? '');
        check('回调应答 ' . ($ack === '' ? '(空)' : $ack), $acked($r), "HTTP {$r['http_code']} " . substr($r['body'] ?? '', 0, 120));
        check('充值单已付款', ($rechargeRow($no)['status'] ?? null) == 1, json_encode($rechargeRow($no), JSON_UNESCAPED_UNICODE));
        check('余额到账 +¥' . RECHARGE_AMOUNT, $gained($before) === RECHARGE_AMOUNT, '实际 +¥' . $gained($before));
        $bill = $lastBill();
        check('账单记一笔「充值」', ($bill['log'] ?? '') === '充值' && cents($bill['amount'] ?? 0) === RECHARGE_AMOUNT, json_encode($bill, JSON_UNESCAPED_UNICODE));

        if (in_array('duplicate', $extras, true)) {
            $after = $member();
            $r = $notify($no);
            check('重复通知不会重复入账', $gained($after) === '0.00' && !$acked($r), substr($r['body'] ?? '', 0, 80));
        }
    }

    echo "\n▶ 充值 · 接口手续费（固定 ¥1.50，买家承担）\n";
    $payId = $newPay('UsdtGateway', 'usdt.trc20', $configIds['Usdt'], true, '1.5');
    $before = $member();
    $res = $recharge($payId, '20');
    $no = (string)($res['data']['tradeNo'] ?? '');
    check('应付 ¥21.50（面额 + 手续费）', cents($res['data']['amount'] ?? 0) === '21.50', json_encode($res, JSON_UNESCAPED_UNICODE));
    $checkGateway($no, RECHARGE_CALLBACK, '21.50');
    $r = $notify($no);
    check('回调成功', $acked($r), substr($r['body'] ?? '', 0, 80));
    check('余额只到账面额 +¥20.00，手续费不进余额', $gained($before) === '20.00', '实际 +¥' . $gained($before));

    // 拿 A 单签名正确的通知去打 B 单的回调地址。两单金额相同，只能靠「报文里的单号必须等于回调地址的单号」拦住
    foreach (['Yipay.mapi' => 'wxpay', 'Wechat' => 'native', 'Usdt' => 'usdt.trc20'] as $configLabel => $code) {
        [$handle] = $configs[$configLabel];
        echo "\n▶ 充值 · 串单重放（{$handle}）\n";
        $payId = $newPay($handle, $code, $configIds[$configLabel], true);
        $a = (string)($recharge($payId, '10')['data']['tradeNo'] ?? '');
        $b = (string)($recharge($payId, '10')['data']['tradeNo'] ?? '');
        $before = $member();
        $r = $notify($a, ['to' => SITE . RECHARGE_CALLBACK . $b]);
        check('A 单的合法通知投到 B 单回调被拒绝，余额不变',
            $a !== '' && $b !== '' && ($rechargeRow($b)['status'] ?? null) == 0 && ($rechargeRow($a)['status'] ?? null) == 0 && $gained($before) === '0.00' && !$acked($r),
            "HTTP {$r['http_code']} " . substr($r['body'] ?? '', 0, 80));
        $r = $notify($a);
        check('A 单的通知投回 A 单本身仍能正常到账', ($rechargeRow($a)['status'] ?? null) == 1 && $gained($before) === '10.00', '实际 +¥' . $gained($before));
    }

    // ============================================================ 主动查单
    // 模拟「平台已收款但通知一直没到」：/_mock/pay 只在模拟器里把单标成已付款、不发通知，再由本站去查单
    echo "\n━━ 主动查单（通知丢失时兜底） ━━\n";
    $query = function (string $callbackPath, string $no): array {
        $res = http('POST', SITE . $callbackPath . $no, ['pay_query' => 1]);
        return ['code' => $res['code'], 'body' => trim($res['body']), 'msg' => (string)((json_decode($res['body'], true) ?: [])['msg'] ?? '')];
    };
    // 服务端同一单 10 秒内只查一次，连续测试前先清掉限流记录
    $unthrottle = function (): void {
        foreach (glob(BASE . '/runtime/pay-query/*') ?: [] as $f) {
            @unlink($f);
        }
    };
    $mockPay = fn(string $no, ?string $amount = null) => http('GET', MOCK . '/_mock/pay?' . http_build_query(['order' => $no] + ($amount === null ? [] : ['amount' => $amount])));
    $mockQueries = fn(string $no) => (int)(((json_decode(http('GET', MOCK . '/_mock/orders')['body'], true) ?: [])[$no] ?? [])['queries'] ?? 0);
    $queryAcked = fn(array $q) => in_array($q['body'], ['success', '{"code":"SUCCESS","message":"成功"}'], true);
    $querySupported = fn(string $configLabel) => in_array($configs[$configLabel][0], ['Yipay', 'AlipayOpen', 'WechatPayV3'], true);
    $unthrottle();

    $throttleTested = false;
    foreach ($scenarios as [$name, $configLabel, $code, $mode, $expect]) {
        if (!$querySupported($configLabel)) {
            continue;
        }
        echo "\n▶ 查单 · {$name}\n";
        [$handle] = $configs[$configLabel];
        $payId = $newPay($handle, $code, $configIds[$configLabel]);
        $res = $order($payId);
        $no = (string)($res['data']['tradeNo'] ?? '');
        if ($no === '') {
            check('下单', false, $res['msg'] ?? json_encode($res, JSON_UNESCAPED_UNICODE));
            continue;
        }
        $page = $walk($mode, (string)$res['data']['url'], $expect);
        if ($page !== '') {
            preg_match('/data-query="([^"]*)"/', $page, $qm);
            check('收银台页面会触发查单', ($qm[1] ?? '') === ORDER_CALLBACK . $no, $qm[1] ?? '(无 data-query)');
        }

        $q = $query(ORDER_CALLBACK, $no);
        check('买家还没付款：查单回「尚未付款」', !$queryAcked($q) && str_contains($q['msg'], '尚未付款') && ($orderRow($no)['status'] ?? null) == 0, $q['body']);
        if (!$throttleTested) {
            $throttleTested = true;
            $before = $mockQueries($no);
            $q = $query(ORDER_CALLBACK, $no);
            check('10 秒内重复查单被限流，不打到平台', str_contains($q['msg'], '频繁') && $mockQueries($no) === $before, $q['body'] . " 平台查单次数 {$before} → " . $mockQueries($no));
        }

        $mockPay($no);
        $unthrottle();
        $q = $query(ORDER_CALLBACK, $no);
        $row = $orderRow($no);
        check('平台已收款但通知丢失：查单后订单已付款', $queryAcked($q) && ($row['status'] ?? null) == 1, $q['body'] . ' ' . json_encode($row, JSON_UNESCAPED_UNICODE));
        check('已自动发货', !empty($row['secret']), (string)($row['secret'] ?? ''));

        $unthrottle();
        $q = $query(ORDER_CALLBACK, $no);
        check('已付款的单不再查平台', !$queryAcked($q) && str_contains($q['msg'], '已支付'), $q['body']);
    }

    echo "\n▶ 查单 · 平台实收金额不符\n";
    $payId = $newPay('Yipay', 'wxpay', $configIds['Yipay.mapi']);
    $no = (string)($order($payId)['data']['tradeNo'] ?? '');
    $mockPay($no, '0.01');
    $unthrottle();
    $q = $query(ORDER_CALLBACK, $no);
    check('平台说已付款但金额不对，系统拒绝发货', !$queryAcked($q) && ($orderRow($no)['status'] ?? null) == 0, $q['body']);

    echo "\n▶ 查单 · 支付宝查单应答验签失败\n";
    $payId = $newPay('AlipayOpen', 'face', $configIds['Alipay']);
    $no = (string)($order($payId)['data']['tradeNo'] ?? '');
    $mockPay($no);
    $pdo->prepare("UPDATE {$t('pay')} SET pay_config_id = ? WHERE id = ?")->execute([$configIds['Alipay.wrongkey'], $payId]);
    $unthrottle();
    $q = $query(ORDER_CALLBACK, $no);
    check('应答签名对不上支付宝公钥时不认，订单保持未付款', !$queryAcked($q) && str_contains($q['msg'], '验签不通过') && ($orderRow($no)['status'] ?? null) == 0, $q['body']);
    $pdo->prepare("UPDATE {$t('pay')} SET pay_config_id = ? WHERE id = ?")->execute([$configIds['Alipay'], $payId]);
    $unthrottle();
    $q = $query(ORDER_CALLBACK, $no);
    check('换回正确的公钥后同一笔单查单成功', $queryAcked($q) && ($orderRow($no)['status'] ?? null) == 1, $q['body']);

    echo "\n▶ 查单 · 超过 24 小时的订单\n";
    $payId = $newPay('WechatPayV3', 'native', $configIds['Wechat']);
    $no = (string)($order($payId)['data']['tradeNo'] ?? '');
    $mockPay($no);
    $pdo->prepare("UPDATE {$t('order')} SET create_time = ? WHERE trade_no = ?")->execute([date('Y-m-d H:i:s', time() - 25 * 3600), $no]);
    $unthrottle();
    $before = $mockQueries($no);
    $q = $query(ORDER_CALLBACK, $no);
    check('不再主动查单，也不打到平台', str_contains($q['msg'], '24 小时') && $mockQueries($no) === $before && ($orderRow($no)['status'] ?? null) == 0, $q['body']);

    foreach ([['Yipay.mapi', 'wxpay', '易支付'], ['Alipay', 'face', '支付宝'], ['Wechat', 'native', '微信']] as [$configLabel, $code, $name]) {
        echo "\n▶ 查单 · 充值 · {$name}\n";
        [$handle] = $configs[$configLabel];
        $payId = $newPay($handle, $code, $configIds[$configLabel], true);
        $res = $recharge($payId, RECHARGE_AMOUNT);
        $no = (string)($res['data']['tradeNo'] ?? '');
        $page = http('GET', SITE . ($res['data']['url'] ?? ''))['body'];
        preg_match('/data-query="([^"]*)"/', $page, $qm);
        check('收银台页面会触发查单（充值回调）', ($qm[1] ?? '') === RECHARGE_CALLBACK . $no, $qm[1] ?? '(无 data-query)');
        $before = $member();
        $q = $query(RECHARGE_CALLBACK, $no);
        check('还没付款：余额不变', !$queryAcked($q) && $gained($before) === '0.00', $q['body']);
        $mockPay($no);
        $unthrottle();
        $q = $query(RECHARGE_CALLBACK, $no);
        check('通知丢失时查单入账 +¥' . RECHARGE_AMOUNT, $queryAcked($q) && ($rechargeRow($no)['status'] ?? null) == 1 && $gained($before) === RECHARGE_AMOUNT,
            $q['body'] . ' 实际 +¥' . $gained($before));
        $unthrottle();
        $q = $query(RECHARGE_CALLBACK, $no);
        check('再查不会重复入账', !$queryAcked($q) && $gained($before) === RECHARGE_AMOUNT, $q['body']);
    }

    echo "\n▶ 查单 · 定时任务 PayKit/cron.php\n";
    $orderNo = (string)($order($newPay('Yipay', 'wxpay', $configIds['Yipay.mapi']))['data']['tradeNo'] ?? '');
    $rechargeNo = (string)($recharge($newPay('WechatPayV3', 'native', $configIds['Wechat'], true), RECHARGE_AMOUNT)['data']['tradeNo'] ?? '');
    $mockPay($orderNo);
    $mockPay($rechargeNo);
    $unthrottle();
    $before = $member();
    $cron = fn() => (string)shell_exec('php ' . escapeshellarg(BASE . '/app/Pay/PayKit/cron.php') . ' --site=' . escapeshellarg(SITE)
        . ' --state=' . escapeshellarg(MOCK_DIR . '/cron-state.json') . ' 2>&1');
    $out = $cron();
    echo preg_replace('/^/m', '    │ ', trim($out)), "\n";
    check('定时任务查到商品订单已付款并发货', ($orderRow($orderNo)['status'] ?? null) == 1 && !empty($orderRow($orderNo)['secret']) && str_contains($out, "#{$orderNo} 已付款"), $orderNo);
    check('定时任务查到充值单已付款并入账', ($rechargeRow($rechargeNo)['status'] ?? null) == 1 && $gained($before) === RECHARGE_AMOUNT && str_contains($out, "#{$rechargeNo} 已付款"), $rechargeNo);
    $out = $cron();
    check('一分钟内再跑：刚查过的未付款单按间隔跳过', str_contains($out, '本次查单 0 笔'), trim($out));
} finally {
    if (!$keep) {
        [$orders, $users] = cleanup($pdo, $t);
        printf("\n已清理 %d 笔测试订单、%d 个临时会员（含充值单/账单/会话）及测试支付接口/配置\n", $orders, $users);
    }
    config_set($pdo, $t('config'), 'trade_verification', (string)$tradeVerification);
    proc_terminate($mock);
}

$failed = array_filter($results, fn($r) => !$r[1]);
printf("\n%d 项检查，%d 通过，%d 失败%s\n", count($results), count($results) - count($failed), count($failed), $keep ? '（--keep：测试数据已保留）' : '（测试数据已清理）');
exit($failed ? 1 : 0);
