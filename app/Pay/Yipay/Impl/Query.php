<?php
declare(strict_types=1);

namespace App\Pay\Yipay\Impl;

use App\Pay\PayKit\Http;
use App\Pay\PayKit\Queryable;
use Kernel\Exception\JSONException;

class Query implements Queryable
{
    /**
     * 彩虹易支付查单：GET api.php?act=order&pid=&key=&out_trade_no=
     * 返回 code=1 时带 trade_no、out_trade_no、type、money、status（1 已付款）；code=-1 是订单号不存在，即买家还没去付款页。
     * 这个接口的应答不带签名，可信度靠 HTTPS 和只有本商户知道的 key，所以网关地址请务必用 https。
     */
    public function query(string $tradeNo, array $config): ?array
    {
        $pid = (string)($config['pid'] ?? '');
        $response = Http::request('GET', Pay::gateway((string)($config['url'] ?? '')) . '/api.php', ['query' => [
            'act' => 'order',
            'pid' => $pid,
            'key' => (string)($config['key'] ?? ''),
            'out_trade_no' => $tradeNo,
        ]]);
        $data = Http::json($response['body']);

        $code = (int)($data['code'] ?? 0);
        if ($code === -1) {
            return null;
        }
        if ($code !== 1) {
            throw new JSONException("易支付查单失败：" . ($data['msg'] ?? ('HTTP ' . $response['status'])));
        }
        if ((string)($data['out_trade_no'] ?? '') !== $tradeNo || (int)($data['status'] ?? 0) !== 1) {
            return null;
        }

        return [
            'pid' => $pid,
            'trade_no' => (string)($data['trade_no'] ?? ''),
            'out_trade_no' => $tradeNo,
            'type' => (string)($data['type'] ?? ''),
            'money' => (string)($data['money'] ?? ''),
            'trade_status' => 'TRADE_SUCCESS',
        ];
    }
}
