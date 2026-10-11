<?php
declare(strict_types=1);

namespace App\Pay\Yipay\Impl;

use App\Pay\PayKit\ActiveQuery;
use App\Pay\PayKit\Sign;

class Signature implements \App\Pay\Signature
{
    /**
     * 易支付异步通知（GET）：pid、trade_no、out_trade_no、type、name、money、trade_status、sign、sign_type
     * 带 pay_query 的请求是主动查单，不是通知，见 PayKit\ActiveQuery
     */
    public function verification(array $data, array $config): bool
    {
        if (ActiveQuery::requested($data)) {
            return ActiveQuery::run('Yipay', new Query(), $config);
        }

        $key = (string)($config['key'] ?? '');
        if ($key === '' || !isset($data['sign'])) {
            return false;
        }

        //同一个回调地址只认本商户的通知
        if (isset($data['pid']) && (string)$data['pid'] !== (string)($config['pid'] ?? '')) {
            return false;
        }

        return Sign::equals(Sign::md5($data, $key, Pay::SIGN_EXCLUDE), $data['sign']);
    }
}
