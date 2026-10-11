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
    /** 交易类型 => [币种, PayKit\Checkout::CHAINS 的键] */
    private const TRADE_TYPES = [
        'usdt.trc20' => ['USDT', 'tron'],
        'usdt.bep20' => ['USDT', 'bsc'],
        'usdt.erc20' => ['USDT', 'ethereum'],
        'usdt.polygon' => ['USDT', 'polygon'],
        'usdt.arbitrum' => ['USDT', 'arbitrum'],
        'usdt.solana' => ['USDT', 'solana'],
        'usdt.aptos' => ['USDT', 'aptos'],
        'usdt.ton' => ['USDT', 'ton'],
        'usdc.polygon' => ['USDC', 'polygon'],
        'usdc.bep20' => ['USDC', 'bsc'],
        'tron.trx' => ['TRX', 'tron'],
    ];

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
        if (($this->config['checkout'] ?? 'local') === 'gateway') {
            return Checkout::redirect($url);
        }
        return $this->localCheckout((array)$data['data'], $url);
    }

    /**
     * 本站收银台：网关下单时已经返回了收款地址（token）、应付币数（actual_amount）和有效期，直接在本站展示，
     * 附上转账说明和提醒；网关收银台链接留作备用。
     * 收银台模式（cashier）、GM Pay 这类下单时还没定网络的，展示说明后让买家点按钮去网关收银台选。
     */
    private function localCheckout(array $order, string $cashierUrl): PayEntity
    {
        $extra = [
            'cny' => number_format($this->amount, 2, '.', ''),
            'expires' => time() + max(60, (int)($order['expiration_time'] ?? $this->config['timeout'] ?? 1200)),
            'cashier_url' => $cashierUrl,
            'return_url' => $this->returnUrl,
        ];
        $address = trim((string)($order['token'] ?? ''));
        $amount = trim((string)($order['actual_amount'] ?? ''));
        [$asset, $chain] = self::TRADE_TYPES[$this->code] ?? ['USDT', ''];
        //GM Pay 下单不带网络，返回的地址不代表买家要用的链，一律去网关收银台选
        $gm = ($this->config['protocol'] ?? 'epusdt') === 'gmpay';

        if ($gm || $chain === '' || $address === '' || $amount === '' || !is_numeric($amount)) {
            return Checkout::crypto('cashier', $asset, '', '', '', $extra + ['label' => $asset . ' 收银台']);
        }
        return Checkout::crypto('chain', $asset, $chain, $address, $amount, $extra);
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
