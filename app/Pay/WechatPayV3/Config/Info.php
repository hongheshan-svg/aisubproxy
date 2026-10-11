<?php
declare(strict_types=1);

/**
 * 微信支付 APIv3（直连商户）
 *
 * 接口：/v3/pay/transactions/native、/v3/pay/transactions/h5；回调为 TRANSACTION.SUCCESS 加密通知。
 * 请求签名、应答/回调验签、AEAD_AES_256_GCM 解密规则对照微信支付官方 APIv3 文档与 wechatpay-php SDK。
 */
return [
    'name' => '微信支付官方',
    'version' => '1.0.0',
    'author' => 'aisubproxy',
    'description' => '微信支付 APIv3 直连：Native 扫码、H5 支付',
    'options' => [
        'native' => 'Native 扫码支付',
        'h5' => 'H5 支付（手机浏览器）',
    ],
    'callback' => [
        \App\Consts\Pay::IS_SIGN => true,
        \App\Consts\Pay::IS_STATUS => true,
        //以下字段由 Impl/Signature 解密通知密文后写回
        \App\Consts\Pay::FIELD_STATUS_KEY => 'trade_state',
        \App\Consts\Pay::FIELD_STATUS_VALUE => 'SUCCESS',
        \App\Consts\Pay::FIELD_ORDER_KEY => 'out_trade_no',
        \App\Consts\Pay::FIELD_AMOUNT_KEY => 'total_yuan',
        \App\Consts\Pay::FIELD_RESPONSE => '{"code":"SUCCESS","message":"成功"}',
    ],
];
