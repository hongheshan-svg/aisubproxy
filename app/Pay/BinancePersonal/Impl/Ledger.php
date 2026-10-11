<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use Kernel\Exception\JSONException;

/**
 * 金额台账：给每笔待付款订单分一个在本通道内独一无二的 USDT 金额，到账时按金额认领。
 *
 * - 同一通道（某个链上网络，或币安 Pay）里，金额从「人民币 ÷ 汇率，向上取到分」开始，被占用就加 0.01 顺延。
 * - 超时后再保留 GRACE 秒：这段时间里迟到的付款仍能认领，这个金额也不会分给新订单，免得把旧单的钱算到新单头上。
 * - 已付款的单也一直占到期满，理由同上（买家重复转账时不会误认到新单）。
 * - 一笔到账（txid）只能认领一笔订单。
 *
 * 存在 runtime/binance-personal/ledger.json，读改写都在文件锁里完成，并发下单不会分到同一个金额。
 */
final class Ledger
{
    public const GRACE = 1800;

    /** txid 认领记录保留多久 */
    private const USED_TTL = 3 * 86400;

    private const FILE = Binance::DIR . '/ledger.json';

    /**
     * @return array{channel:string, cents:int, cny:string, created:int, expires:int}
     * @throws JSONException
     */
    public static function allocate(string $tradeNo, string $channel, int $baseCents, string $cny, int $timeout): array
    {
        return self::locked(function (array &$ledger) use ($tradeNo, $channel, $baseCents, $cny, $timeout): array {
            if (isset($ledger['orders'][$tradeNo])) {
                return $ledger['orders'][$tradeNo];
            }

            $held = [];
            foreach ($ledger['orders'] as $order) {
                if ($order['channel'] === $channel) {
                    $held[$order['cents']] = true;
                }
            }
            $cents = max(1, $baseCents);
            for ($i = 0; isset($held[$cents]); $i++, $cents++) {
                if ($i >= 1000) {
                    throw new JSONException('待付款的订单太多，请稍后再试');
                }
            }

            $now = time();
            return $ledger['orders'][$tradeNo] = [
                'channel' => $channel,
                'cents' => $cents,
                'cny' => $cny,
                'created' => $now,
                'expires' => $now + $timeout,
            ];
        });
    }

    public static function find(string $tradeNo): ?array
    {
        $fp = @fopen(self::FILE, 'r');
        if ($fp === false) {
            return null;
        }
        //共享锁：别和正在写台账的请求读到半截文件
        flock($fp, LOCK_SH);
        $ledger = json_decode((string)stream_get_contents($fp), true);
        flock($fp, LOCK_UN);
        fclose($fp);
        $order = is_array($ledger) ? ($ledger['orders'][$tradeNo] ?? null) : null;
        return is_array($order) ? $order : null;
    }

    /**
     * 把一笔到账记到某笔订单名下；已被别的订单认领过返回 false
     */
    public static function claim(string $txid, string $tradeNo): bool
    {
        return self::locked(function (array &$ledger) use ($txid, $tradeNo): bool {
            $owner = $ledger['used'][$txid]['trade_no'] ?? null;
            if ($owner !== null && $owner !== $tradeNo) {
                return false;
            }
            $ledger['used'][$txid] = ['trade_no' => $tradeNo, 'at' => time()];
            return true;
        });
    }

    /**
     * 在排他文件锁里读-改-写台账，顺带清掉过期的占用和认领记录
     */
    private static function locked(callable $change): mixed
    {
        if (!is_dir(Binance::DIR)) {
            @mkdir(Binance::DIR, 0755, true);
        }
        $fp = fopen(self::FILE, 'c+');
        if ($fp === false || !flock($fp, LOCK_EX)) {
            throw new JSONException('币安收款台账无法写入，请检查 runtime 目录权限');
        }
        try {
            $ledger = json_decode((string)stream_get_contents($fp), true);
            $ledger = is_array($ledger) ? $ledger : [];
            $ledger += ['orders' => [], 'used' => []];

            $now = time();
            $ledger['orders'] = array_filter($ledger['orders'], fn($o) => is_array($o) && $o['expires'] + self::GRACE >= $now);
            $ledger['used'] = array_filter($ledger['used'], fn($u) => is_array($u) && $u['at'] + self::USED_TTL >= $now);

            $result = $change($ledger);

            ftruncate($fp, 0);
            rewind($fp);
            fwrite($fp, json_encode($ledger, JSON_UNESCAPED_SLASHES));
            fflush($fp);
            return $result;
        } finally {
            flock($fp, LOCK_UN);
            fclose($fp);
        }
    }
}
