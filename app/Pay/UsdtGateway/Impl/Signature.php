<?php
declare(strict_types=1);

namespace App\Pay\UsdtGateway\Impl;

use App\Pay\PayKit\Sign;
use Kernel\Exception\JSONException;

class Signature implements \App\Pay\Signature
{
    /** 支付成功的状态值；BEpusdt 还会推送 1（等待支付，每分钟一次）和 3（已超时） */
    private const PAID = 2;

    /**
     * 网关回调（JSON）：trade_id、order_id、amount、actual_amount、token/receive_address、block_transaction_id、status、signature
     *
     * 签名按原始 JSON 重新解析后计算——数字要保留网关发来的类型（2 而不是 "2"、28.88 而不是 "28.88"），
     * 拼接写法才能和网关一致。
     *
     * BEpusdt 判断回调是否送达只看 HTTP 状态码是不是 200，而系统拒绝回调时回的也是 200。
     * 所以验签失败时这里改回 HTTP 500，网关才会按退避策略重发（最多 10 次），配置填错时也能在网关后台看到失败记录。
     */
    public function verification(array $data, array $config): bool
    {
        if (!$this->verified($data, $config)) {
            http_response_code(500);
            return false;
        }

        //等待支付、已超时的状态推送：签名没问题就直接回 200 了事，不进系统的回调流程，免得每分钟记一条「状态验证失败」
        $payload = json_decode((string)file_get_contents('php://input'), true);
        $status = (int)((is_array($payload) ? $payload : $data)['status'] ?? 0);
        if ($status !== self::PAID) {
            throw new JSONException("收到网关的状态通知（status={$status}），无需处理");
        }
        return true;
    }

    private function verified(array $data, array $config): bool
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
