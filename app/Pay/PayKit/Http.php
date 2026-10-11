<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

use GuzzleHttp\Client;
use GuzzleHttp\Exception\GuzzleException;
use Kernel\Exception\JSONException;

/**
 * 支付网关 HTTP 调用。
 *
 * 不用 Base::http()：那里关掉了 TLS 证书校验，下单接口被中间人篡改（换掉收款二维码）是实打实的风险。
 * 这里默认校验证书；自建 USDT 网关用自签证书时，插件可显式传 $verify = false。
 */
final class Http
{
    /**
     * @param string $method
     * @param string $url
     * @param array $options Guzzle 请求选项（headers / body / form_params / json）
     * @param bool $verify 是否校验 TLS 证书
     * @return array{status:int, headers:array<string,string>, body:string}
     * @throws JSONException 网络层失败（连不上、超时、证书错误）
     */
    public static function request(string $method, string $url, array $options = [], bool $verify = true): array
    {
        $client = new Client([
            'timeout' => 20,
            'connect_timeout' => 8,
            'http_errors' => false,
            'verify' => $verify,
        ]);

        try {
            $response = $client->request($method, $url, $options);
        } catch (GuzzleException $e) {
            throw new JSONException("支付网关连接失败：" . self::shorten($e->getMessage()));
        }

        $headers = [];
        foreach ($response->getHeaders() as $name => $values) {
            $headers[strtolower((string)$name)] = implode(', ', $values);
        }

        return [
            'status' => $response->getStatusCode(),
            'headers' => $headers,
            'body' => (string)$response->getBody(),
        ];
    }

    /**
     * 解析 JSON 响应体，解析不了就当网关异常
     *
     * @throws JSONException
     */
    public static function json(string $body): array
    {
        $data = json_decode($body, true);
        if (!is_array($data)) {
            throw new JSONException("支付网关返回了无法识别的内容：" . self::shorten($body));
        }
        return $data;
    }

    public static function shorten(string $text, int $length = 200): string
    {
        $text = trim(preg_replace('/\s+/', ' ', strip_tags($text)) ?? '');
        return mb_strlen($text) > $length ? mb_substr($text, 0, $length) . '…' : $text;
    }

    /**
     * 后台填的网关地址统一成「不带末尾斜杠」
     */
    public static function base(string $url): string
    {
        return rtrim(trim($url), '/');
    }
}
