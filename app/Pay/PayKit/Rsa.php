<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

use Kernel\Exception\JSONException;

/**
 * SHA256withRSA 签名/验签（支付宝 RSA2、微信支付 APIv3 共用）。
 *
 * 后台配置框里粘贴的密钥格式五花八门：支付宝密钥工具导出的是不带头尾的一整行 base64，
 * 微信下载的是 PEM 文件内容，有人还会粘贴整张证书。这里统一兜住。
 */
final class Rsa
{
    /**
     * @param string $key PEM、裸 base64（PKCS#8 / PKCS#1）均可
     * @return \OpenSSLAsymmetricKey
     * @throws JSONException
     */
    public static function privateKey(string $key): \OpenSSLAsymmetricKey
    {
        foreach (self::candidates($key, ['PRIVATE KEY', 'RSA PRIVATE KEY']) as $pem) {
            $res = openssl_pkey_get_private($pem);
            if ($res !== false) {
                return $res;
            }
        }
        throw new JSONException("商户私钥格式不正确，请检查支付配置");
    }

    /**
     * @param string $key PEM 公钥、裸 base64 公钥，或 X.509 证书（平台证书）
     * @return \OpenSSLAsymmetricKey
     * @throws JSONException
     */
    public static function publicKey(string $key): \OpenSSLAsymmetricKey
    {
        foreach (self::candidates($key, ['PUBLIC KEY', 'RSA PUBLIC KEY', 'CERTIFICATE']) as $pem) {
            $res = openssl_pkey_get_public($pem);
            if ($res !== false) {
                return $res;
            }
        }
        throw new JSONException("平台公钥格式不正确，请检查支付配置");
    }

    /**
     * @return string base64 签名
     * @throws JSONException
     */
    public static function sign(string $data, string $privateKey): string
    {
        if (!openssl_sign($data, $signature, self::privateKey($privateKey), OPENSSL_ALGO_SHA256)) {
            throw new JSONException("签名失败，请检查商户私钥");
        }
        return base64_encode($signature);
    }

    public static function verify(string $data, string $signature, string $publicKey): bool
    {
        $raw = base64_decode($signature, true);
        if ($raw === false || $raw === '') {
            return false;
        }
        try {
            return openssl_verify($data, $raw, self::publicKey($publicKey), OPENSSL_ALGO_SHA256) === 1;
        } catch (JSONException) {
            return false;
        }
    }

    /**
     * 已是 PEM 就原样用；否则去掉空白后按候选头尾逐个包一层试
     *
     * @return string[]
     */
    private static function candidates(string $key, array $labels): array
    {
        $key = trim(str_replace(["\r\n", "\r"], "\n", $key));
        if ($key === '') {
            return [];
        }
        if (str_contains($key, '-----BEGIN')) {
            return [$key];
        }
        $body = chunk_split((string)preg_replace('/\s+/', '', $key), 64, "\n");
        return array_map(fn(string $label) => "-----BEGIN {$label}-----\n{$body}-----END {$label}-----", $labels);
    }
}
