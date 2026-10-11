<?php
declare(strict_types=1);

/**
 * 易支付（彩虹易支付 V1 协议，MD5 签名）
 *
 * 协议参考：彩虹易支付开发文档 submit.php / mapi.php；实现对照 assimon/dujiaoka 的 YipayController。
 * 绝大多数「易支付」平台、以及 GM Pay（新版 epusdt）的 /payments/epay/v1 兼容接口都走这一套。
 */
return [
    'name' => '易支付',
    'version' => '1.0.0',
    'author' => 'aisubproxy',
    'description' => '彩虹易支付协议（MD5）：支付宝、微信、QQ钱包、云闪付',
    'options' => [
        'alipay' => '支付宝',
        'wxpay' => '微信支付',
        'qqpay' => 'QQ钱包',
        'bank' => '云闪付',
    ],
    'callback' => [
        \App\Consts\Pay::IS_SIGN => true,
        \App\Consts\Pay::IS_STATUS => true,
        \App\Consts\Pay::FIELD_STATUS_KEY => 'trade_status',
        \App\Consts\Pay::FIELD_STATUS_VALUE => 'TRADE_SUCCESS',
        \App\Consts\Pay::FIELD_ORDER_KEY => 'out_trade_no',
        \App\Consts\Pay::FIELD_AMOUNT_KEY => 'money',
        \App\Consts\Pay::FIELD_RESPONSE => 'success',
    ],
];
