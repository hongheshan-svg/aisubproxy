<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use App\Pay\PayKit\Http;
use Kernel\Exception\JSONException;

/**
 * USDT 兑人民币汇率。后台「汇率」一栏的写法沿用 BEpusdt：
 *
 * - 留空（或 auto）：自动取 CoinGecko 上 USDT 的人民币实时价
 * - 数字，如 7.20：固定汇率
 * - ~1.02：实时价 × 1.02
 * - +0.05 / -0.05：实时价加减
 *
 * 实时价缓存 10 分钟；接口暂时连不上时，6 小时内取到过的价格仍然可用，再久就报错，提示改成固定汇率。
 * 最终汇率不在 1～100 之间一律拒绝下单，防止接口抽风把东西按白菜价卖出去。
 */
final class Rate
{
    public const SOURCE = 'https://api.coingecko.com/api/v3/simple/price?ids=tether&vs_currencies=cny';

    private const TTL = 600;
    private const STALE = 21600;

    /**
     * @return float 1 USDT 合多少元，保留 4 位小数
     * @throws JSONException
     */
    public static function resolve(array $config): float
    {
        $syntax = str_replace(' ', '', trim((string)($config['rate'] ?? '')));

        //固定汇率只认不带符号的数字：is_numeric 会把 +0.05、-0.05 也当数字，那是「实时价加减」
        if (preg_match('/^\d+(\.\d+)?$/D', $syntax) === 1) {
            $rate = (float)$syntax;
        } elseif ($syntax === '' || strtolower($syntax) === 'auto' || preg_match('/^[~+-]\d+(\.\d+)?$/D', $syntax) === 1) {
            $market = self::market($config);
            $operand = (float)substr($syntax, 1);
            $rate = match ($syntax[0] ?? '') {
                '~' => $market * $operand,
                '+' => $market + $operand,
                '-' => $market - $operand,
                default => $market,
            };
        } else {
            throw new JSONException('币安收款的汇率格式不正确：留空为自动，或填 7.20、~1.02、+0.05、-0.05 这样的写法');
        }

        $rate = round($rate, 4);
        if ($rate < 1 || $rate > 100) {
            throw new JSONException("USDT 汇率 {$rate} 不在合理范围内，请检查后台「汇率」设置");
        }
        return $rate;
    }

    /**
     * CoinGecko 上 USDT 的人民币实时价（带缓存）
     *
     * @throws JSONException
     */
    private static function market(array $config): float
    {
        $source = trim((string)($config['rate_api'] ?? '')) ?: self::SOURCE;
        $file = Binance::DIR . '/rate-' . substr(md5($source), 0, 12) . '.json';
        $cached = json_decode((string)@file_get_contents($file), true);
        $age = is_array($cached) ? time() - (int)($cached['at'] ?? 0) : PHP_INT_MAX;
        if ($age < self::TTL) {
            return (float)$cached['rate'];
        }

        try {
            $response = Http::request('GET', $source, ['headers' => ['Accept' => 'application/json']]);
            $price = Http::json($response['body'])['tether']['cny'] ?? null;
            if ($response['status'] !== 200 || !is_numeric($price) || (float)$price < 1 || (float)$price > 100) {
                throw new JSONException('HTTP ' . $response['status'] . ' ' . Http::shorten($response['body'], 100));
            }
        } catch (JSONException $e) {
            if ($age < self::STALE) {
                return (float)$cached['rate'];
            }
            throw new JSONException('自动获取 USDT 汇率失败（' . $e->getMessage() . '），可以先在后台把「汇率」改成固定值');
        }

        if (!is_dir(Binance::DIR)) {
            @mkdir(Binance::DIR, 0755, true);
        }
        @file_put_contents($file, json_encode(['at' => time(), 'rate' => (float)$price]), LOCK_EX);
        return (float)$price;
    }
}
