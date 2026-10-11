<?php
declare(strict_types=1);

return [
    [
        'title' => 'APPID',
        'name' => 'app_id',
        'type' => 'input',
        'placeholder' => '支付宝开放平台应用的 APPID',
    ],
    [
        'title' => '应用私钥',
        'name' => 'private_key',
        'type' => 'textarea',
        'height' => 120,
        'placeholder' => '支付宝密钥工具生成的「应用私钥」（RSA2），PEM 或单行 base64 均可',
    ],
    [
        'title' => '支付宝公钥',
        'name' => 'alipay_public_key',
        'type' => 'textarea',
        'height' => 100,
        'placeholder' => '开放平台「接口加签方式」里的支付宝公钥（不是应用公钥）',
    ],
    [
        'title' => '网关地址',
        'name' => 'gateway',
        'type' => 'input',
        'placeholder' => '留空即正式环境 https://openapi.alipay.com/gateway.do；沙箱填 https://openapi-sandbox.dl.alipaydev.com/gateway.do',
    ],
];
