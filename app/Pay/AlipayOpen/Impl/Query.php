<?php
declare(strict_types=1);

namespace App\Pay\AlipayOpen\Impl;

use App\Pay\PayKit\Queryable;
use Kernel\Exception\JSONException;

class Query implements Queryable
{
    /**
     * alipay.trade.query：按商户单号查交易。
     * 成功应答已在 Pay::call 里验签；TRADE_SUCCESS / TRADE_FINISHED 才算已付款。
     * ACQ.TRADE_NOT_EXIST 表示买家还没扫码（当面付）或还没打开收银台，按未付款处理。
     */
    public function query(string $tradeNo, array $config): ?array
    {
        $node = Pay::call($config, 'alipay.trade.query', ['out_trade_no' => $tradeNo], '查单', [], $tradeNo);

        if ((string)($node['code'] ?? '') !== '10000') {
            if ((string)($node['sub_code'] ?? '') === 'ACQ.TRADE_NOT_EXIST') {
                return null;
            }
            throw new JSONException("支付宝查单失败：" . ($node['sub_msg'] ?? $node['msg'] ?? '未知错误'));
        }

        if ((string)($node['out_trade_no'] ?? '') !== $tradeNo
            || !in_array((string)($node['trade_status'] ?? ''), ['TRADE_SUCCESS', 'TRADE_FINISHED'], true)) {
            return null;
        }

        return [
            'app_id' => trim((string)($config['app_id'] ?? '')),
            'out_trade_no' => $tradeNo,
            'trade_no' => (string)($node['trade_no'] ?? ''),
            'total_amount' => (string)($node['total_amount'] ?? ''),
            'trade_status' => 'TRADE_SUCCESS',
        ];
    }
}
