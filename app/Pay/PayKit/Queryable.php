<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

use Kernel\Exception\JSONException;

/**
 * 支持主动查单的插件在 Impl/Query.php 里实现这个接口，PayKit/cron.php 也按这个文件是否存在判断插件能不能查单。
 */
interface Queryable
{
    /**
     * 按本站订单号去支付平台查单
     *
     * @param string $tradeNo 本站订单号（下单时传给平台的商户单号）
     * @param array $config 这笔订单所用的那套支付配置
     * @return array|null 已付款时返回与本插件异步通知同名的字段（单号、状态、金额，系统按 Config/Info.php 的 callback 读取）；
     *                    未付款或平台上查无此单返回 null
     * @throws JSONException 查单失败：连不上平台、配置有误、应答验签不通过
     */
    public function query(string $tradeNo, array $config): ?array;
}
