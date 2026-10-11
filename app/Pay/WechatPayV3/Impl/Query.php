<?php
declare(strict_types=1);

namespace App\Pay\WechatPayV3\Impl;

use App\Pay\PayKit\Queryable;
use Kernel\Exception\JSONException;

class Query implements Queryable
{
    /**
     * 商户订单号查单：GET /v3/pay/transactions/out-trade-no/{out_trade_no}?mchid=
     * 200 应答已在 Pay::request 里验签；trade_state=SUCCESS 才算已付款，404 ORDER_NOT_EXIST 表示还没下到微信（按未付款处理）。
     * 返回字段与回调解密后写回上下文的字段一致。
     */
    public function query(string $tradeNo, array $config): ?array
    {
        $mchid = trim((string)($config['mchid'] ?? ''));
        $path = '/v3/pay/transactions/out-trade-no/' . rawurlencode($tradeNo) . '?mchid=' . rawurlencode($mchid);
        $response = Pay::request($config, 'GET', $path, null, '查单', $tradeNo);
        $data = $response['data'];

        if ($response['status'] === 404) {
            return null;
        }
        if ($response['status'] !== 200) {
            throw new JSONException("微信支付查单失败：" . ($data['message'] ?? ('HTTP ' . $response['status'])));
        }

        if ((string)($data['out_trade_no'] ?? '') !== $tradeNo
            || (string)($data['mchid'] ?? '') !== $mchid
            || (string)($data['appid'] ?? '') !== trim((string)($config['appid'] ?? ''))
            || (string)($data['trade_state'] ?? '') !== 'SUCCESS') {
            return null;
        }

        $total = (int)($data['amount']['total'] ?? -1);
        return [
            'out_trade_no' => $tradeNo,
            'trade_state' => 'SUCCESS',
            'total_yuan' => $total < 0 ? '' : number_format($total / 100, 2, '.', ''),
            'transaction_id' => (string)($data['transaction_id'] ?? ''),
        ];
    }
}
