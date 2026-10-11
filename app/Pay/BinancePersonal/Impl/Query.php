<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use App\Pay\PayKit\Queryable;

class Query implements Queryable
{
    /** 允许币安记录时间比本站下单时间早这么多毫秒（两边时钟差） */
    private const CLOCK_SKEW = 5000;

    /**
     * 在币安到账记录里找这笔单：通道（网络或 Pay）、金额（精确到分）都对上，到账时间不早于下单时间，
     * 且这笔到账没被别的订单认领过。认领成功就当已付款，金额回报下单时记下的人民币金额，交给系统核对
     */
    public function query(string $tradeNo, array $config): ?array
    {
        $entry = Ledger::find($tradeNo);
        if ($entry === null) {
            return null;
        }

        $created = (int)$entry['created'] * 1000;
        foreach (Binance::records($config, (string)$entry['channel'], $created - 60000) as $record) {
            if (!$record['ok'] || $record['channel'] !== $entry['channel'] || $record['cents'] !== (int)$entry['cents']
                || $record['time'] < $created - self::CLOCK_SKEW) {
                continue;
            }
            if (!Ledger::claim((string)$record['txid'], $tradeNo)) {
                continue;
            }
            return [
                'order_id' => $tradeNo,
                'status' => 'paid',
                'amount' => (string)$entry['cny'],
                'txid' => (string)$record['txid'],
                'usdt' => number_format((int)$entry['cents'] / 100, 2, '.', ''),
            ];
        }
        return null;
    }
}
