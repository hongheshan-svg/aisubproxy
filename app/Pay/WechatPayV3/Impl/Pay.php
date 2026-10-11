<?php
declare(strict_types=1);

namespace App\Pay\WechatPayV3\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use App\Pay\PayKit\Http;
use App\Pay\PayKit\Rsa;
use App\Util\PayConfig;
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

        $response = self::request($this->config, 'POST', '/v3/pay/transactions/' . $this->code, $body, '下单', $this->tradeNo);
        $data = $response['data'];
        if ($response['status'] !== 200) {
            throw new JSONException("微信支付下单失败：" . ($data['message'] ?? ('HTTP ' . $response['status'])));
        }

        if ($this->code === 'native') {
            $codeUrl = (string)($data['code_url'] ?? '');
            if ($codeUrl === '') {
                throw new JSONException("微信支付下单失败：未返回 code_url");
            }
            return Checkout::qrcode($codeUrl, 'wechat', $this->returnUrl, '', $this->callbackUrl);
        }

        $h5Url = (string)($data['h5_url'] ?? '');
        if ($h5Url === '') {
            throw new JSONException("微信支付下单失败：未返回 h5_url");
        }
        //付款后微信会跳回 redirect_url；从本站收银台跳过去，Referer 才是商户在微信登记的 H5 域名
        $h5Url .= (str_contains($h5Url, '?') ? '&' : '?') . 'redirect_url=' . urlencode($this->returnUrl);
        return Checkout::jump($h5Url, 'wechat', $this->returnUrl, $this->callbackUrl);
    }

    /**
     * 带 APIv3 签名调用接口。200 应答必须验签通过（防止支付链接、查单结果被中间人篡改），否则抛错；
     * 其他状态码原样返回，由调用方按业务处理（如查单的 404 ORDER_NOT_EXIST）
     *
     * @param string $path 含查询串的接口路径，签名串里用的就是它
     * @param array|null $body POST 的 JSON 主体；GET 传 null
     * @param string $label 出错提示里的动作名：下单 / 查单
     * @return array{status:int, data:array}
     * @throws JSONException
     */
    public static function request(array $config, string $method, string $path, ?array $body, string $label, string $tradeNo = ''): array
    {
        $mchid = trim((string)($config['mchid'] ?? ''));
        $serial = trim((string)($config['serial_no'] ?? ''));
        if ($mchid === '' || $serial === '' || trim((string)($config['private_key'] ?? '')) === '') {
            throw new JSONException("微信支付商户号、证书序列号或商户私钥未配置");
        }

        $json = $body === null ? '' : json_encode($body, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        $timestamp = (string)time();
        $nonce = bin2hex(random_bytes(16));
        $signature = Rsa::sign("{$method}\n{$path}\n{$timestamp}\n{$nonce}\n{$json}\n", (string)$config['private_key']);

        $options = ['headers' => [
            'Authorization' => sprintf(
                'WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"',
                $mchid, $nonce, $signature, $timestamp, $serial
            ),
            'Accept' => 'application/json',
            'User-Agent' => 'acg-faka WechatPayV3/1.0',
        ]];
        if ($body !== null) {
            $options['headers']['Content-Type'] = 'application/json';
            $options['body'] = $json;
        }

        $response = Http::request($method, self::apiBase($config) . $path, $options);
        PayConfig::log('WechatPayV3', 'TRADE', "{$method} {$path} {$tradeNo} HTTP {$response['status']} " . Http::shorten($response['body'], 500));

        $data = Http::json($response['body'] === '' ? '{}' : $response['body']);
        if ($response['status'] === 200) {
            $h = $response['headers'];
            if (!Signature::verifyMessage(
                $h['wechatpay-timestamp'] ?? '', $h['wechatpay-nonce'] ?? '', $response['body'],
                $h['wechatpay-signature'] ?? '', $h['wechatpay-serial'] ?? '', $config
            )) {
                throw new JSONException("微信支付{$label}失败：应答验签不通过，请检查「微信支付公钥」与公钥ID");
            }
        }

        return ['status' => $response['status'], 'data' => $data];
    }

    public static function apiBase(array $config): string
    {
        $base = Http::base((string)($config['api_base'] ?? ''));
        return $base === '' ? self::API_BASE : $base;
    }
}
