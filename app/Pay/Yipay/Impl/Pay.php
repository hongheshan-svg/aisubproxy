<?php
declare(strict_types=1);

namespace App\Pay\Yipay\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use App\Pay\PayKit\Http;
use App\Pay\PayKit\Sign;
use App\Util\Client;
use Kernel\Exception\JSONException;

class Pay extends Base implements \App\Pay\Pay
{
    /**
     * 签名不参与的字段（彩虹易支付规范）
     */
    public const SIGN_EXCLUDE = ['sign', 'sign_type'];

    private const BRANDS = ['alipay' => 'alipay', 'wxpay' => 'wechat', 'qqpay' => 'qq', 'bank' => 'bank'];

    /**
     * @return PayEntity
     * @throws JSONException
     */
    public function trade(): PayEntity
    {
        $gateway = self::gateway((string)($this->config['url'] ?? ''));
        $key = (string)($this->config['key'] ?? '');

        $params = [
            'pid' => (string)($this->config['pid'] ?? ''),
            'type' => $this->code,
            'out_trade_no' => $this->tradeNo,
            'notify_url' => $this->callbackUrl,
            'return_url' => $this->returnUrl,
            'name' => '订单' . $this->tradeNo,
            'money' => number_format($this->amount, 2, '.', ''),
        ];

        if (($this->config['mode'] ?? 'submit') !== 'mapi') {
            $params['sign'] = Sign::md5($params, $key, self::SIGN_EXCLUDE);
            $params['sign_type'] = 'MD5';
            return Checkout::submit($gateway . '/submit.php', $params);
        }

        $params['clientip'] = $this->clientIp;
        $params['device'] = Client::isMobile() ? 'mobile' : 'pc';
        $params['sign'] = Sign::md5($params, $key, self::SIGN_EXCLUDE);
        $params['sign_type'] = 'MD5';

        $response = Http::request('POST', $gateway . '/mapi.php', ['form_params' => $params]);
        $this->log("mapi {$this->tradeNo} HTTP {$response['status']} " . Http::shorten($response['body'], 500));
        $data = Http::json($response['body']);

        if ((int)($data['code'] ?? 0) !== 1) {
            throw new JSONException("易支付下单失败：" . ($data['msg'] ?? '未知错误'));
        }

        $brand = self::BRANDS[$this->code] ?? '';
        if (!empty($data['qrcode'])) {
            return Checkout::qrcode((string)$data['qrcode'], $brand, $this->returnUrl, '', $this->callbackUrl);
        }
        $url = (string)($data['payurl'] ?? $data['urlscheme'] ?? '');
        if ($url === '') {
            throw new JSONException("易支付下单失败：网关未返回支付地址");
        }
        return Checkout::jump($url, $brand, $this->returnUrl, $this->callbackUrl);
    }

    /**
     * 后台常有人把 submit.php（或 mapi.php、api.php）一起填进来，统一去掉
     */
    public static function gateway(string $url): string
    {
        $url = Http::base($url);
        if ($url === '') {
            throw new JSONException("易支付网关地址未配置");
        }
        return (string)preg_replace('#/(submit|mapi|api)\.php$#i', '', $url);
    }
}
