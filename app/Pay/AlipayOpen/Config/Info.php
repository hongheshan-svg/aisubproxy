<?php
declare(strict_types=1);

/**
 * 支付宝开放平台（RSA2，公钥模式）
 *
 * 接口：alipay.trade.precreate（当面付）、alipay.trade.page.pay（电脑网站）、alipay.trade.wap.pay（手机网站）。
 * 签名与验签规则对照支付宝官方 SDK 与 yansongda/pay 实现。
 */
return [
    'name' => '支付宝官方',
    'version' => '1.0.0',
    'author' => 'aisubproxy',
    'description' => '支付宝开放平台直连（RSA2）：当面付、电脑/手机网站支付',
    'options' => [
        'face' => '当面付（扫码）',
        'web' => '网站支付（自动区分电脑/手机）',
        'page' => '电脑网站支付',
        'wap' => '手机网站支付',
    ],
    'callback' => [
        \App\Consts\Pay::IS_SIGN => true,
        \App\Consts\Pay::IS_STATUS => true,
        //TRADE_FINISHED 也算成功，验签时已统一成 TRADE_SUCCESS
        \App\Consts\Pay::FIELD_STATUS_KEY => 'trade_status',
        \App\Consts\Pay::FIELD_STATUS_VALUE => 'TRADE_SUCCESS',
        \App\Consts\Pay::FIELD_ORDER_KEY => 'out_trade_no',
        \App\Consts\Pay::FIELD_AMOUNT_KEY => 'total_amount',
        \App\Consts\Pay::FIELD_RESPONSE => 'success',
    ],
];
