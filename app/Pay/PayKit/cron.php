<?php
declare(strict_types=1);

/**
 * 主动查单定时任务，建议每分钟跑一次。
 *
 * 找出最近 24 小时内还没付款、且支付插件支持查单（有 Impl/Query.php）的商品订单和充值单，
 * 逐笔向本站支付回调地址 POST pay_query=1，由插件去支付平台查单（见 ActiveQuery）。
 * 平台确认已付款的，系统照常核对金额后发货/入账，和收到异步通知完全一样。
 *
 *   php app/Pay/PayKit/cron.php [--site=http://127.0.0.1] [--host=域名] [--limit=30] [--state=状态文件] [--base=站点根目录]
 *
 * --site   本机访问站点的地址，默认 http://127.0.0.1（Docker 镜像里的 nginx）。宝塔等环境填站点域名
 * --host   用 IP 访问、但站点按域名区分虚拟主机时，指定 Host 头
 * --limit  每次最多查几笔，默认 30
 * --state  记录每笔单上次查询时间的文件，默认系统临时目录下 acg-pay-query.json
 * --base   站点根目录，一般不用填
 *
 * Docker 部署：在宿主机 crontab 里加一行
 *   * * * * * docker exec faka-local php /var/www/html/app/Pay/PayKit/cron.php >/dev/null 2>&1
 *
 * 查询频率随订单年龄放慢：10 分钟内的单每分钟查一次，之后间隔约为年龄的 1/10，最长 30 分钟查一次，
 * 免得大量弃单把支付平台的查单接口刷满。
 */

if (PHP_SAPI !== 'cli') {
    http_response_code(404);
    exit;
}

const ORDER_CALLBACK = '/user/api/order/callback.';
const RECHARGE_CALLBACK = '/user/api/rechargeNotification/callback.';

date_default_timezone_set('Asia/Shanghai'); //与站点一致，订单 create_time 存的是这个时区的时间

$opts = getopt('', ['site::', 'host::', 'limit::', 'state::', 'base::']);

//站点根目录。Docker 镜像里 app/Pay 是指向 /data/pay 的软链，__DIR__ 会解析成软链目标，推不出站点根，
//所以依次尝试：--base、镜像的 ACG_HOME 环境变量、按命令行给的脚本路径往上三级、__DIR__ 往上三级
foreach ([$opts['base'] ?? '', (string)getenv('ACG_HOME'), dirname((string)($_SERVER['SCRIPT_FILENAME'] ?? '')) . '/../../..', __DIR__ . '/../../..'] as $dir) {
    if ($dir !== '' && is_file($dir . '/config/database.php')) {
        define('BASE', rtrim($dir, '/'));
        break;
    }
}
if (!defined('BASE')) {
    fwrite(STDERR, "找不到站点根目录，请用 --base=站点根目录 指定\n");
    exit(1);
}
$site = rtrim((string)($opts['site'] ?? 'http://127.0.0.1'), '/');
$host = (string)($opts['host'] ?? '');
$limit = max(1, (int)($opts['limit'] ?? 30));
$stateFile = (string)($opts['state'] ?? sys_get_temp_dir() . '/acg-pay-query.json');

/** 下一次查询前至少要隔多久：随订单年龄放慢，50 秒 ~ 30 分钟 */
function interval(int $age): int
{
    return max(50, min(1800, intdiv($age, 10)));
}

$handles = [];
foreach (glob(BASE . '/app/Pay/*/Impl/Query.php') ?: [] as $file) {
    $handles[] = basename(dirname($file, 2));
}
if (!$handles) {
    echo "没有支持主动查单的支付插件\n";
    exit(0);
}

$db = require BASE . '/config/database.php';
$pdo = new PDO("mysql:host={$db['host']};port={$db['port']};dbname={$db['database']};charset=utf8mb4", $db['username'], $db['password'],
    [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION, PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC]);
$t = fn(string $name) => $db['prefix'] . $name;

$since = date('Y-m-d H:i:s', time() - 86400);
$in = implode(',', array_fill(0, count($handles), '?'));
$candidates = [];
foreach ([['order', ORDER_CALLBACK, '订单'], ['user_recharge', RECHARGE_CALLBACK, '充值单']] as [$table, $callback, $label]) {
    $stmt = $pdo->prepare("SELECT o.trade_no, o.create_time FROM {$t($table)} o JOIN {$t('pay')} p ON p.id = o.pay_id
        WHERE o.status = 0 AND o.create_time >= ? AND p.handle IN ({$in}) ORDER BY o.id DESC LIMIT 500");
    $stmt->execute(array_merge([$since], $handles));
    foreach ($stmt->fetchAll() as $row) {
        $candidates[] = ['no' => (string)$row['trade_no'], 'created' => (int)strtotime((string)$row['create_time']), 'callback' => $callback, 'label' => $label];
    }
}

$state = json_decode((string)@file_get_contents($stateFile), true) ?: [];
$now = time();
foreach ($state as $no => $at) {
    if ($now - (int)$at > 2 * 86400) {
        unset($state[$no]);
    }
}

$queried = $paid = 0;
foreach ($candidates as $c) {
    if ($queried >= $limit) {
        break;
    }
    if ($now - (int)($state[$c['no']] ?? 0) < interval($now - $c['created'])) {
        continue;
    }
    $state[$c['no']] = $now;
    $queried++;

    $ch = curl_init($site . $c['callback'] . $c['no']);
    $headers = ['User-Agent: acg-faka pay-query cron'];
    if ($host !== '') {
        $headers[] = "Host: {$host}";
    }
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_POSTFIELDS => 'pay_query=1',
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 30,
    ]);
    $body = trim((string)curl_exec($ch));
    $error = curl_error($ch);
    curl_close($ch);

    //回调成功时系统回的是插件约定的应答（success / 微信的 SUCCESS JSON），其余是 {"code":0,"msg":"..."}
    $json = json_decode($body, true);
    if ($body === 'success' || (is_array($json) && ($json['code'] ?? null) === 'SUCCESS')) {
        $paid++;
        printf("%s #%s 已付款，已交给系统发货/入账\n", $c['label'], $c['no']);
    } else {
        $msg = $error !== '' ? "请求失败：{$error}" : (is_array($json) ? (string)($json['msg'] ?? $body) : mb_substr($body, 0, 120));
        if ($msg === 'fail') {
            //平台说已付款，但系统核对没过（金额不符等），具体原因系统已写进该支付插件目录下的 runtime.log
            $msg = '平台返回已付款，但系统核对未通过（如金额不符），未发货，原因见支付插件的 runtime.log';
        }
        printf("%s #%s %s\n", $c['label'], $c['no'], $msg);
    }
}

@file_put_contents($stateFile, json_encode($state));
printf("本次查单 %d 笔，确认付款 %d 笔（候选 %d 笔，支持查单的插件：%s）\n", $queried, $paid, count($candidates), implode('、', $handles));
