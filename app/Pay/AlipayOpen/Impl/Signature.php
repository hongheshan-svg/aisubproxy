<?php
declare(strict_types=1);

namespace App\Pay\AlipayOpen\Impl;

use App\Pay\PayKit\ActiveQuery;
use App\Pay\PayKit\Rsa;
use App\Pay\PayKit\Sign;
use Kernel\Util\Context;

class Signature implements \App\Pay\Signature
{
    /**
     * 支付宝异步通知（POST 表单）验签：去掉 sign、sign_type，其余参数排序拼接后用支付宝公钥 RSA2 验证
     * 带 pay_query 的请求是主动查单，不是通知，见 PayKit\ActiveQuery
     */
    public function verification(array $data, array $config): bool
    {
        if (ActiveQuery::requested($data)) {
            return ActiveQuery::run('AlipayOpen', new Query(), $config);
        }

        $publicKey = (string)($config['alipay_public_key'] ?? '');
        if (trim($publicKey) === '' || empty($data['sign'])) {
            return false;
        }
        if (isset($data['sign_type']) && strtoupper((string)$data['sign_type']) !== 'RSA2') {
            return false;
        }
        //只认发给本应用的通知
        if ((string)($data['app_id'] ?? '') !== trim((string)($config['app_id'] ?? ''))) {
            return false;
        }

        if (!Rsa::verify(Sign::canonical($data, ['sign', 'sign_type']), (string)$data['sign'], $publicKey)) {
            return false;
        }

        //交易完结（TRADE_FINISHED）同样代表已付款，统一成系统按单值比对的 TRADE_SUCCESS
        if (($data['trade_status'] ?? '') === 'TRADE_FINISHED') {
            $data['trade_status'] = 'TRADE_SUCCESS';
            Context::set(\App\Consts\Pay::DAFA, $data);
        }
        return true;
    }
}
