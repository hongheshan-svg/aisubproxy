<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use App\Pay\PayKit\Http;
use Kernel\Exception\JSONException;

/**
 * 币安现货 API（只读）：充值地址、充值记录、Pay 交易记录。
 *
 * 签名：把全部参数（含 timestamp、recvWindow）拼成查询串，用 Secret Key 做 HMAC-SHA256，
 * 结果作为 signature 参数追加在最后；API Key 放在 X-MBX-APIKEY 请求头。
 */
final class Binance
{
    public const API_BASE = 'https://api.binance.com';

    /** 支付方式 code => [币安网络代码, 展示名] */
    public const NETWORKS = [
        'trc20' => ['TRX', 'USDT-TRC20'],
        'bep20' => ['BSC', 'USDT-BEP20'],
        'solana' => ['SOL', 'USDT-Solana'],
        'arbitrum' => ['ARBITRUM', 'USDT-Arbitrum'],
        'erc20' => ['ETH', 'USDT-ERC20'],
    ];

    /** 币安 Pay 转账在台账里的通道名 */
    public const PAY = 'PAY';

    public const DIR = BASE_PATH . '/runtime/binance-personal';

    /** 到账记录缓存秒数：Pay 交易记录接口一次就占 3000 权重（每分钟上限 180000），多笔待付款订单共用一次查询 */
    private const RECORDS_TTL = 8;

    /** 充值地址缓存秒数 */
    private const ADDRESS_TTL = 3600;

    /**
     * 本账户 USDT 在某个网络上的充值地址。从币安接口取，不让站长手填，免得地址填错收不到钱
     *
     * @throws JSONException
     */
    public static function depositAddress(array $config, string $network): string
    {
        $file = self::DIR . '/address-' . $network . '-' . self::account($config) . '.json';
        $cached = json_decode((string)@file_get_contents($file), true);
        if (is_array($cached) && ($cached['at'] ?? 0) > time() - self::ADDRESS_TTL && ($cached['address'] ?? '') !== '') {
            return (string)$cached['address'];
        }

        $data = self::get($config, '/sapi/v1/capital/deposit/address', ['coin' => 'USDT', 'network' => $network]);
        $address = trim((string)($data['address'] ?? ''));
        if ($address === '') {
            throw new JSONException("币安没有返回 {$network} 网络的 USDT 充值地址，请确认该网络可以充值");
        }
        if (($data['tag'] ?? '') !== '') {
            throw new JSONException("{$network} 网络充值需要填写 Memo，暂不支持");
        }
        self::write($file, ['at' => time(), 'address' => $address]);
        return $address;
    }

    /**
     * 某个通道 $since（毫秒）之后的到账记录，统一成：
     * [txid, channel（网络代码或 PAY）, cents（金额折成分；有分以下的零头为 null）, ok（已到账且是 USDT）, time（毫秒）]
     *
     * @throws JSONException
     */
    public static function records(array $config, string $channel, int $since): array
    {
        $file = self::DIR . '/records-' . ($channel === self::PAY ? 'pay' : 'deposit') . '-' . self::account($config) . '.json';
        $cached = json_decode((string)@file_get_contents($file), true);
        if (is_array($cached) && ($cached['at'] ?? 0) >= time() - self::RECORDS_TTL && ($cached['since'] ?? PHP_INT_MAX) <= $since) {
            return (array)$cached['records'];
        }

        $records = [];
        if ($channel === self::PAY) {
            $data = self::get($config, '/sapi/v1/pay/transactions', ['startTime' => $since, 'limit' => 100]);
            if (($data['success'] ?? true) === false) {
                throw new JSONException('币安接口错误：' . ($data['message'] ?? '查询 Pay 交易记录失败'));
            }
            foreach ((array)($data['data'] ?? []) as $tx) {
                //amount 为正是收入、为负是支出；只认别人直接转给你的（C2C）和付款（PAY），退款、红包都不算
                $records[] = [
                    'txid' => 'pay:' . ($tx['transactionId'] ?? ''),
                    'channel' => self::PAY,
                    'cents' => self::cents((string)($tx['amount'] ?? '')),
                    'ok' => ($tx['currency'] ?? '') === 'USDT' && in_array($tx['orderType'] ?? '', ['C2C', 'PAY'], true)
                        && (float)($tx['amount'] ?? 0) > 0,
                    'time' => (int)($tx['transactionTime'] ?? 0),
                ];
            }
        } else {
            $data = self::get($config, '/sapi/v1/capital/deposit/hisrec', ['coin' => 'USDT', 'startTime' => $since, 'limit' => 1000]);
            foreach ($data as $deposit) {
                //status：1 成功，6 已入账但暂不能提现（都算收到了）；0 确认中、2 拒绝、7 充错、8 待确认都不算
                $records[] = [
                    'txid' => 'deposit:' . ($deposit['txId'] ?? $deposit['id'] ?? ''),
                    'channel' => (string)($deposit['network'] ?? ''),
                    'cents' => self::cents((string)($deposit['amount'] ?? '')),
                    'ok' => ($deposit['coin'] ?? '') === 'USDT' && in_array((int)($deposit['status'] ?? -1), [1, 6], true),
                    'time' => (int)($deposit['insertTime'] ?? 0),
                ];
            }
        }

        self::write($file, ['at' => time(), 'since' => $since, 'records' => $records]);
        return $records;
    }

    /**
     * "0.98000000" → 98；分以下有零头（比如 0.985）返回 null，这种金额不可能和任何订单对上
     */
    public static function cents(string $amount): ?int
    {
        if (preg_match('/^(\d+)(?:\.(\d+))?$/D', trim($amount), $m) !== 1) {
            return null;
        }
        $fraction = $m[2] ?? '';
        if (rtrim(substr($fraction, 2), '0') !== '') {
            return null;
        }
        return (int)$m[1] * 100 + (int)str_pad(substr($fraction, 0, 2), 2, '0');
    }

    /**
     * @throws JSONException
     */
    private static function get(array $config, string $path, array $params): array
    {
        $key = trim((string)($config['api_key'] ?? ''));
        $secret = trim((string)($config['api_secret'] ?? ''));
        if ($key === '' || $secret === '') {
            throw new JSONException('币安 API Key 或 Secret Key 未配置');
        }

        $query = http_build_query($params + ['recvWindow' => 10000, 'timestamp' => (int)floor(microtime(true) * 1000)]);
        $query .= '&signature=' . hash_hmac('sha256', $query, $secret);
        $response = Http::request('GET', self::base($config) . $path . '?' . $query, ['headers' => ['X-MBX-APIKEY' => $key]]);

        $data = Http::json($response['body']);
        if ($response['status'] !== 200) {
            //常见：-2015 Key/IP/权限不对，-1021 服务器时间不准
            throw new JSONException('币安接口错误：' . ($data['msg'] ?? $data['message'] ?? ('HTTP ' . $response['status'])));
        }
        return $data;
    }

    private static function base(array $config): string
    {
        $base = Http::base((string)($config['api_base'] ?? ''));
        return $base === '' ? self::API_BASE : $base;
    }

    /** 缓存文件按 Key + Secret 区分：换了账户不会读到上一个账户的地址和记录，Secret 填错也会立刻请求接口暴露出来 */
    private static function account(array $config): string
    {
        return substr(md5(trim((string)($config['api_key'] ?? '')) . "\n" . trim((string)($config['api_secret'] ?? ''))), 0, 12);
    }

    private static function write(string $file, array $data): void
    {
        if (!is_dir(self::DIR)) {
            @mkdir(self::DIR, 0755, true);
        }
        @file_put_contents($file, json_encode($data, JSON_UNESCAPED_SLASHES), LOCK_EX);
    }
}
