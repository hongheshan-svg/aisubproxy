<?php
declare(strict_types=1);

namespace App\Pay\UsdtGateway\Impl;

use App\Pay\PayKit\Sign;

class Signature implements \App\Pay\Signature
{
    /**
     * 网关回调（JSON）：trade_id、order_id、amount、actual_amount、token/receive_address、block_transaction_id、status、signature
     *
     * 签名按原始 JSON 重新解析后计算——数字要保留网关发来的类型（2 而不是 "2"、28.88 而不是 "28.88"），
     * 拼接写法才能和网关一致。
     */
    public function verification(array $data, array $config): bool
    {
        $token = (string)($config['token'] ?? '');
        $payload = json_decode((string)file_get_contents('php://input'), true);
        if (!is_array($payload) || !isset($payload['signature'])) {
            $payload = $data;
        }
        if (trim($token) === '' || empty($payload['signature'])) {
            return false;
        }

        if (($config['protocol'] ?? 'epusdt') === 'gmpay') {
            if ((string)($payload['pid'] ?? '') !== trim((string)($config['pid'] ?? ''))) {
                return false;
            }
            $expected = Sign::hmacSha256($payload, $token, ['signature']);
        } else {
            $expected = Sign::md5($payload, $token, ['signature']);
        }

        return Sign::equals($expected, $payload['signature']);
    }
}
