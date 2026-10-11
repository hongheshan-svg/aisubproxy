<?php
declare(strict_types=1);

namespace App\Pay\UsdtGateway\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use App\Pay\PayKit\Http;
use App\Pay\PayKit\Sign;
use Kernel\Exception\JSONException;

class Pay extends Base implements \App\Pay\Pay
{
    /**
     * @return PayEntity
     * @throws JSONException
     */
    public function trade(): PayEntity
    {
        $token = (string)($this->config['token'] ?? '');
        if (trim($token) === '') {
            throw new JSONException("收U网关 API Token 未配置");
        }

        //金额按两位小数传数字；签名按网关（Go）对 JSON 数字的写法拼接，见 Sign::scalar()
        $amount = round($this->amount, 2);
        $gm = ($this->config['protocol'] ?? 'epusdt') === 'gmpay';

        if ($gm) {
            //GM Pay 不传 token/network 时创建占位订单，由用户在网关收银台选择币种与网络
            $params = [
                'pid' => trim((string)($this->config['pid'] ?? '')),
                'order_id' => $this->tradeNo,
                'currency' => 'cny',
                'amount' => $amount,
                'notify_url' => $this->callbackUrl,
                'redirect_url' => $this->returnUrl,
                'name' => '订单' . $this->tradeNo,
            ];
            if ($params['pid'] === '') {
                throw new JSONException("GM Pay 需要配置 PID");
            }
            $params['signature'] = Sign::hmacSha256($params, $token, ['signature']);
            $endpoint = self::endpoint((string)($this->config['url'] ?? ''), '/payments/gmpay/v1/order/create-transaction');
        } else {
            $params = [
                'order_id' => $this->tradeNo,
                'amount' => $amount,
                'notify_url' => $this->callbackUrl,
                'redirect_url' => $this->returnUrl,
                'name' => '订单' . $this->tradeNo,
            ];
            $cashier = $this->code === 'cashier';
            if (!$cashier) {
                $params['trade_type'] = $this->code;
            }
            if ((int)($this->config['timeout'] ?? 0) > 0) {
                $params['timeout'] = (int)$this->config['timeout'];
            }
            if (trim((string)($this->config['rate'] ?? '')) !== '') {
                $params['rate'] = trim((string)$this->config['rate']);
            }
            $params['signature'] = Sign::md5($params, $token, ['signature']);
            $endpoint = self::endpoint(
                (string)($this->config['url'] ?? ''),
                $cashier ? '/api/v1/order/create-order' : '/api/v1/order/create-transaction'
            );
        }

        $response = Http::request('POST', $endpoint, ['json' => $params]);
        $this->log("create {$this->tradeNo} {$endpoint} HTTP {$response['status']} " . Http::shorten($response['body'], 500));
        $data = Http::json($response['body']);

        $url = (string)($data['data']['payment_url'] ?? '');
        if ((int)($data['status_code'] ?? 0) !== 200 || $url === '') {
            throw new JSONException("收U网关下单失败：" . ($data['message'] ?? ('HTTP ' . $response['status'])));
        }
        return Checkout::redirect($url);
    }

    /**
     * 后台可以只填网关根地址，也可以填完整接口地址；收银台模式只需要把 create-transaction 换成 create-order
     *
     * @throws JSONException
     */
    public static function endpoint(string $url, string $defaultPath): string
    {
        $url = Http::base($url);
        if ($url === '') {
            throw new JSONException("收U网关地址未配置");
        }
        if (preg_match('#/order/create-(transaction|order)$#', $url)) {
            return (string)preg_replace('#create-(transaction|order)$#', basename($defaultPath), $url);
        }
        return $url . $defaultPath;
    }
}
