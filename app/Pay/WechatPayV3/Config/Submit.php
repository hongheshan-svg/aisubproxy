<?php
declare(strict_types=1);

return [
    [
        'title' => '商户号',
        'name' => 'mchid',
        'type' => 'input',
        'placeholder' => '微信支付商户号（mchid）',
    ],
    [
        'title' => 'AppID',
        'name' => 'appid',
        'type' => 'input',
        'placeholder' => '与商户号绑定的公众号 / 小程序 / 开放平台 AppID',
    ],
    [
        'title' => 'APIv3 密钥',
        'name' => 'apiv3_key',
        'type' => 'password',
        'placeholder' => '商户平台「API安全」里设置的 32 位 APIv3 密钥，用于解密回调',
    ],
    [
        'title' => '商户证书序列号',
        'name' => 'serial_no',
        'type' => 'input',
        'placeholder' => '商户 API 证书的序列号',
    ],
    [
        'title' => '商户 API 私钥',
        'name' => 'private_key',
        'type' => 'textarea',
        'height' => 120,
        'placeholder' => 'apiclient_key.pem 的完整内容',
    ],
    [
        'title' => '微信支付公钥',
        'name' => 'wechatpay_public_key',
        'type' => 'textarea',
        'height' => 100,
        'placeholder' => '「微信支付公钥」pub_key.pem 的内容；老商户也可以粘贴平台证书（wechatpay_xxx.pem）',
    ],
    [
        'title' => '公钥ID / 平台证书序列号',
        'name' => 'wechatpay_serial',
        'type' => 'input',
        'placeholder' => '微信支付公钥ID（PUB_KEY_ID_ 开头）或平台证书序列号，用于核对应答签名来源',
    ],
    [
        'title' => 'API 地址',
        'name' => 'api_base',
        'type' => 'input',
        'placeholder' => '留空即 https://api.mch.weixin.qq.com，一般无需修改',
    ],
];
