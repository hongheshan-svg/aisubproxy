<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use App\Pay\PayKit\ActiveQuery;

class Signature implements \App\Pay\Signature
{
    /**
     * 个人账户没有支付通知可收，这个回调地址只接受主动查单（pay_query），其余请求一律拒绝
     */
    public function verification(array $data, array $config): bool
    {
        if (ActiveQuery::requested($data)) {
            return ActiveQuery::run('BinancePersonal', new Query(), $config);
        }
        return false;
    }
}
