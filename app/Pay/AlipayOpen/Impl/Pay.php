<?php
declare(strict_types=1);

namespace App\Pay\AlipayOpen\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use App\Pay\PayKit\Http;
use App\Pay\PayKit\Rsa;
use App\Pay\PayKit\Sign;
use App\Util\Client;
use Kernel\Exception\JSONException;

class Pay extends Base implements \App\Pay\Pay
{
    public const GATEWAY = 'https://openapi.alipay.com/gateway.do';

    /**
     * @return PayEntity
     * @throws JSONException
     */
    public function trade(): PayEntity
    {
        $code = $this->code === 'web' ? (Client::isMobile() ? 'wap' : 'page') : $this->code;
        $biz = [
            'out_trade_no' => $this->tradeNo,
            'total_amount' => number_format($this->amount, 2, '.', ''),
            'subject' => '订单' . $this->tradeNo,
        ];

        switch ($code) {
            case 'face':
                return $this->precreate($biz);
            case 'page':
                $biz['product_code'] = 'FAST_INSTANT_TRADE_PAY';
                $params = $this->params('alipay.trade.page.pay', $biz, ['return_url' => $this->returnUrl]);
                return Checkout::submit($this->gateway() . '?charset=utf-8', $params);
            case 'wap':
                $biz['product_code'] = 'QUICK_WAP_WAY';
                $biz['quit_url'] = $this->returnUrl;
                $params = $this->params('alipay.trade.wap.pay', $biz, ['return_url' => $this->returnUrl]);
                return Checkout::submit($this->gateway() . '?charset=utf-8', $params);
        }

        throw new JSONException("支付宝不支持该支付方式：{$this->code}");
    }

    /**
     * 当面付：预下单拿到 qr_code，在本站收银台展示
     *
     * @throws JSONException
     */
    private function precreate(array $biz): PayEntity
    {
        $params = $this->params('alipay.trade.precreate', $biz);
        $response = Http::request('POST', $this->gateway() . '?charset=utf-8', ['form_params' => $params]);
        $this->log("precreate {$this->tradeNo} HTTP {$response['status']} " . Http::shorten($response['body'], 500));

        $data = Http::json($response['body']);
        $node = $data['alipay_trade_precreate_response'] ?? $data['error_response'] ?? null;
        if (!is_array($node)) {
            throw new JSONException("支付宝下单失败：网关返回格式异常");
        }

        if ((string)($node['code'] ?? '') !== '10000') {
            throw new JSONException("支付宝下单失败：" . ($node['sub_msg'] ?? $node['msg'] ?? '未知错误'));
        }

        //成功响应必须带签名且验签通过，防止二维码被中间人替换
        $raw = self::rawNode($response['body'], 'alipay_trade_precreate_response');
        if ($raw === null || !Rsa::verify($raw, (string)($data['sign'] ?? ''), (string)$this->config['alipay_public_key'])) {
            throw new JSONException("支付宝下单失败：响应验签不通过，请检查「支付宝公钥」配置");
        }

        $qr = (string)($node['qr_code'] ?? '');
        if ($qr === '') {
            throw new JSONException("支付宝下单失败：未返回二维码");
        }
        //qr.alipay.com 链接在手机浏览器里打开会直接唤起支付宝
        return Checkout::qrcode($qr, 'alipay', $this->returnUrl, $qr);
    }

    /**
     * 公共请求参数 + 签名（请求签名包含 sign_type，只排除 sign）
     *
     * @throws JSONException
     */
    private function params(string $method, array $biz, array $extra = []): array
    {
        $appId = trim((string)($this->config['app_id'] ?? ''));
        $privateKey = (string)($this->config['private_key'] ?? '');
        if ($appId === '' || trim($privateKey) === '') {
            throw new JSONException("支付宝 APPID 或应用私钥未配置");
        }

        $params = array_merge([
            'app_id' => $appId,
            'method' => $method,
            'format' => 'JSON',
            'charset' => 'utf-8',
            'sign_type' => 'RSA2',
            'timestamp' => (new \DateTime('now', new \DateTimeZone('Asia/Shanghai')))->format('Y-m-d H:i:s'),
            'version' => '1.0',
            'notify_url' => $this->callbackUrl,
            'biz_content' => json_encode($biz, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES),
        ], $extra);

        $params['sign'] = Rsa::sign(Sign::canonical($params, ['sign']), $privateKey);
        return $params;
    }

    private function gateway(): string
    {
        $gateway = trim((string)($this->config['gateway'] ?? ''));
        return $gateway === '' ? self::GATEWAY : $gateway;
    }

    /**
     * 取出响应里某个节点的原始 JSON 文本——支付宝对它原样签名，重新编码会对不上
     */
    public static function rawNode(string $body, string $node): ?string
    {
        $pos = strpos($body, '"' . $node . '"');
        if ($pos === false) {
            return null;
        }
        $start = strpos($body, '{', $pos + strlen($node) + 2);
        if ($start === false) {
            return null;
        }

        $depth = 0;
        $inString = false;
        $length = strlen($body);
        for ($i = $start; $i < $length; $i++) {
            $ch = $body[$i];
            if ($inString) {
                if ($ch === '\\') {
                    $i++;
                } elseif ($ch === '"') {
                    $inString = false;
                }
                continue;
            }
            if ($ch === '"') {
                $inString = true;
            } elseif ($ch === '{') {
                $depth++;
            } elseif ($ch === '}' && --$depth === 0) {
                return substr($body, $start, $i - $start + 1);
            }
        }
        return null;
    }
}
