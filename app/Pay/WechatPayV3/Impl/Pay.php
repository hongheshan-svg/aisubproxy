<?php
declare(strict_types=1);

namespace App\Pay\WechatPayV3\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use App\Pay\PayKit\Http;
use App\Pay\PayKit\Rsa;
use Kernel\Exception\JSONException;

class Pay extends Base implements \App\Pay\Pay
{
    public const API_BASE = 'https://api.mch.weixin.qq.com';

    /**
     * @return PayEntity
     * @throws JSONException
     */
    public function trade(): PayEntity
    {
        if (!in_array($this->code, ['native', 'h5'], true)) {
            throw new JSONException("微信支付不支持该支付方式：{$this->code}");
        }

        $body = [
            'appid' => trim((string)($this->config['appid'] ?? '')),
            'mchid' => trim((string)($this->config['mchid'] ?? '')),
            'description' => '订单' . $this->tradeNo,
            'out_trade_no' => $this->tradeNo,
            'notify_url' => $this->callbackUrl,
            'amount' => ['total' => (int)round($this->amount * 100), 'currency' => 'CNY'],
        ];
        if ($this->code === 'h5') {
            $body['scene_info'] = [
                'payer_client_ip' => $this->clientIp,
                'h5_info' => ['type' => 'Wap'],
            ];
        }

        $data = $this->post('/v3/pay/transactions/' . $this->code, $body);

        if ($this->code === 'native') {
            $codeUrl = (string)($data['code_url'] ?? '');
            if ($codeUrl === '') {
                throw new JSONException("微信支付下单失败：未返回 code_url");
            }
            return Checkout::qrcode($codeUrl, 'wechat', $this->returnUrl);
        }

        $h5Url = (string)($data['h5_url'] ?? '');
        if ($h5Url === '') {
            throw new JSONException("微信支付下单失败：未返回 h5_url");
        }
        //付款后微信会跳回 redirect_url；从本站收银台跳过去，Referer 才是商户在微信登记的 H5 域名
        $h5Url .= (str_contains($h5Url, '?') ? '&' : '?') . 'redirect_url=' . urlencode($this->returnUrl);
        return Checkout::jump($h5Url, 'wechat', $this->returnUrl);
    }

    /**
     * 带 APIv3 签名的 POST，并校验应答签名
     *
     * @throws JSONException
     */
    private function post(string $path, array $body): array
    {
        $mchid = trim((string)($this->config['mchid'] ?? ''));
        $serial = trim((string)($this->config['serial_no'] ?? ''));
        if ($mchid === '' || $serial === '' || trim((string)($this->config['private_key'] ?? '')) === '') {
            throw new JSONException("微信支付商户号、证书序列号或商户私钥未配置");
        }

        $json = json_encode($body, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        $timestamp = (string)time();
        $nonce = bin2hex(random_bytes(16));
        $signature = Rsa::sign("POST\n{$path}\n{$timestamp}\n{$nonce}\n{$json}\n", (string)$this->config['private_key']);

        $response = Http::request('POST', self::apiBase($this->config) . $path, [
            'headers' => [
                'Authorization' => sprintf(
                    'WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"',
                    $mchid, $nonce, $signature, $timestamp, $serial
                ),
                'Accept' => 'application/json',
                'Content-Type' => 'application/json',
                'User-Agent' => 'acg-faka WechatPayV3/1.0',
            ],
            'body' => $json,
        ]);
        $this->log("{$path} {$this->tradeNo} HTTP {$response['status']} " . Http::shorten($response['body'], 500));

        $data = Http::json($response['body'] === '' ? '{}' : $response['body']);
        if ($response['status'] !== 200) {
            throw new JSONException("微信支付下单失败：" . ($data['message'] ?? ('HTTP ' . $response['status'])));
        }

        //应答验签：防止返回的支付链接被中间人替换
        $h = $response['headers'];
        if (!Signature::verifyMessage(
            $h['wechatpay-timestamp'] ?? '', $h['wechatpay-nonce'] ?? '', $response['body'],
            $h['wechatpay-signature'] ?? '', $h['wechatpay-serial'] ?? '', $this->config
        )) {
            throw new JSONException("微信支付下单失败：应答验签不通过，请检查「微信支付公钥」与公钥ID");
        }

        return $data;
    }

    public static function apiBase(array $config): string
    {
        $base = Http::base((string)($config['api_base'] ?? ''));
        return $base === '' ? self::API_BASE : $base;
    }
}
