<?php
declare(strict_types=1);

namespace App\Pay\WechatPayV3\Impl;

use App\Pay\PayKit\ActiveQuery;
use App\Pay\PayKit\Rsa;
use Kernel\Util\Context;

class Signature implements \App\Pay\Signature
{
    /**
     * 微信支付回调：先用请求头验签，再用 APIv3 密钥解密 resource，
     * 把解出的交易信息整理成系统按字段名读取的扁平数组写回上下文。
     *
     * 验签/解密失败时返回 HTTP 500：系统默认对拒绝的回调回 200，
     * 而微信 APIv3 把任何 2xx 都当作「已收到」，不会再重试，配置写错时这笔款就收不到通知了。
     *
     * 带 pay_query 的请求是主动查单，不是通知，见 PayKit\ActiveQuery
     */
    public function verification(array $data, array $config): bool
    {
        if (ActiveQuery::requested($data)) {
            return ActiveQuery::run('WechatPayV3', new Query(), $config);
        }

        $body = (string)file_get_contents('php://input');

        $ok = self::verifyMessage(
            (string)($_SERVER['HTTP_WECHATPAY_TIMESTAMP'] ?? ''),
            (string)($_SERVER['HTTP_WECHATPAY_NONCE'] ?? ''),
            $body,
            (string)($_SERVER['HTTP_WECHATPAY_SIGNATURE'] ?? ''),
            (string)($_SERVER['HTTP_WECHATPAY_SERIAL'] ?? ''),
            $config
        );
        $transaction = $ok ? self::decrypt(json_decode($body, true), (string)($config['apiv3_key'] ?? '')) : null;

        if ($transaction === null
            || (string)($transaction['mchid'] ?? '') !== trim((string)($config['mchid'] ?? ''))
            || (string)($transaction['appid'] ?? '') !== trim((string)($config['appid'] ?? ''))) {
            http_response_code(500);
            return false;
        }

        $total = (int)($transaction['amount']['total'] ?? -1);
        Context::set(\App\Consts\Pay::DAFA, [
            'out_trade_no' => (string)($transaction['out_trade_no'] ?? ''),
            'trade_state' => (string)($transaction['trade_state'] ?? ''),
            'total_yuan' => $total < 0 ? '' : number_format($total / 100, 2, '.', ''),
            'transaction_id' => (string)($transaction['transaction_id'] ?? ''),
        ]);
        return true;
    }

    /**
     * 应答/回调验签：签名串 = 时间戳\n随机串\n报文主体\n
     */
    public static function verifyMessage(string $timestamp, string $nonce, string $body, string $signature, string $serial, array $config): bool
    {
        $publicKey = (string)($config['wechatpay_public_key'] ?? '');
        if ($timestamp === '' || $nonce === '' || $signature === '' || trim($publicKey) === '') {
            return false;
        }
        //微信会不定期发探测流量，签名以 WECHATPAY/SIGNTEST/ 开头，必须验签失败
        if (str_starts_with($signature, 'WECHATPAY/SIGNTEST/')) {
            return false;
        }
        $expectSerial = trim((string)($config['wechatpay_serial'] ?? ''));
        if ($expectSerial !== '' && !hash_equals($expectSerial, trim($serial))) {
            return false;
        }
        if (!ctype_digit($timestamp) || abs(time() - (int)$timestamp) > 300) {
            return false;
        }
        return Rsa::verify("{$timestamp}\n{$nonce}\n{$body}\n", $signature, $publicKey);
    }

    /**
     * AEAD_AES_256_GCM 解密回调 resource
     */
    public static function decrypt(mixed $notify, string $apiV3Key): ?array
    {
        $resource = is_array($notify) ? ($notify['resource'] ?? null) : null;
        if (!is_array($resource) || strlen($apiV3Key) !== 32
            || ($resource['algorithm'] ?? '') !== 'AEAD_AES_256_GCM') {
            return null;
        }

        $cipher = base64_decode((string)($resource['ciphertext'] ?? ''), true);
        if ($cipher === false || strlen($cipher) <= 16) {
            return null;
        }
        $plain = openssl_decrypt(
            substr($cipher, 0, -16), 'aes-256-gcm', $apiV3Key, OPENSSL_RAW_DATA,
            (string)($resource['nonce'] ?? ''), substr($cipher, -16), (string)($resource['associated_data'] ?? '')
        );
        if ($plain === false) {
            return null;
        }
        $data = json_decode($plain, true);
        return is_array($data) ? $data : null;
    }
}
