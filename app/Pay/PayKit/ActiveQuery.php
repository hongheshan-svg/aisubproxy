<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

use App\Model\Order;
use App\Model\UserRecharge;
use App\Util\PayConfig;
use Kernel\Consts\Base;
use Kernel\Exception\JSONException;
use Kernel\Util\Context;

/**
 * 主动查单：支付平台的异步通知迟迟不到（服务器宕机、回调被拦、平台重试用完）时，由本站去平台查这笔单。
 *
 * 入口复用系统的支付回调地址：往 /user/api/order/callback.<单号>（充值是 /user/api/rechargeNotification/callback.<单号>）
 * POST pay_query=1。插件的 Signature 看到这个字段就不按通知验签，而是调用这里去平台查单；平台确认已付款时，
 * 把查到的结果整理成和异步通知同名的字段写回上下文，后面的单号核对、金额逐分核对、重复通知拦截、
 * 串行化事务、发货/入账全部走系统原有流程，和收到真通知一模一样。
 *
 * 会发 pay_query 的有两处：买家停在本站收银台时的页面轮询，以及 PayKit/cron.php 定时任务。
 * 这个地址谁都能请求，所以这里只做「查」，并且只查：本插件的、未付款的、24 小时内创建的订单，同一单 10 秒内最多查一次。
 * 未付款或不能查时抛 JSONException，系统直接以 JSON 回给请求方，不算验签失败，不写回调失败日志。
 */
final class ActiveQuery
{
    public const FIELD = 'pay_query';

    /** 只查这么久以内创建的订单，更早的单平台一般也已关闭 */
    public const WINDOW = 86400;

    /** 同一笔单两次查单的最短间隔（秒） */
    public const MIN_INTERVAL = 10;

    public const THROTTLE_DIR = BASE_PATH . '/runtime/pay-query';

    public static function requested(array $data): bool
    {
        return isset($data[self::FIELD]);
    }

    /**
     * @param string $handle 插件目录名
     * @param Queryable $gateway 插件的查单实现
     * @param array $config 这笔订单所用的支付配置（系统传给 Signature::verification 的那份）
     * @return bool 平台确认已付款，查单结果已写回上下文
     * @throws JSONException 未付款、不能查或查单失败
     */
    public static function run(string $handle, Queryable $gateway, array $config): bool
    {
        $tradeNo = (string)($_GET['_PARAMETER'][0] ?? '');
        if (preg_match('/^\d{1,32}$/D', $tradeNo) !== 1) {
            throw new JSONException('订单号不正确');
        }

        $recharge = stripos((string)Context::get(Base::ROUTE), '/rechargeNotification/') !== false;
        $order = ($recharge ? UserRecharge::query() : Order::query())->with('pay')->where('trade_no', $tradeNo)->first();
        if (!$order || !$order->pay || (string)$order->pay->handle !== $handle) {
            throw new JSONException('订单不存在');
        }
        if ((int)$order->status !== 0) {
            throw new JSONException('订单已支付');
        }
        $created = strtotime((string)$order->create_time);
        if ($created === false || time() - $created > self::WINDOW) {
            throw new JSONException('订单创建已超过 24 小时，不再主动查单');
        }
        if (!self::acquire($handle . ':' . $tradeNo)) {
            throw new JSONException('查单太频繁，请稍后再试');
        }

        $fields = $gateway->query($tradeNo, $config);
        if ($fields === null) {
            throw new JSONException('尚未付款');
        }

        PayConfig::log($handle, 'QUERY', ($recharge ? '充值单' : '订单') . " {$tradeNo} 主动查单：平台确认已付款，交给系统核对金额后处理");
        Context::set(\App\Consts\Pay::DAFA, $fields);
        return true;
    }

    /**
     * 同一笔单 MIN_INTERVAL 秒内只放行一次。读-改-写在同一把文件锁内完成，并发请求也只有一个能拿到
     */
    private static function acquire(string $key): bool
    {
        if (!is_dir(self::THROTTLE_DIR)) {
            @mkdir(self::THROTTLE_DIR, 0755, true);
        }
        $fp = @fopen(self::THROTTLE_DIR . '/' . md5($key), 'c+');
        if ($fp === false) {
            return true; //记不下来就不拦，查单本身没有副作用
        }
        try {
            if (!flock($fp, LOCK_EX)) {
                return true;
            }
            $last = (int)stream_get_contents($fp);
            $now = time();
            if ($now - $last < self::MIN_INTERVAL) {
                return false;
            }
            ftruncate($fp, 0);
            rewind($fp);
            fwrite($fp, (string)$now);
            fflush($fp);
            return true;
        } finally {
            flock($fp, LOCK_UN);
            fclose($fp);
        }
    }
}
