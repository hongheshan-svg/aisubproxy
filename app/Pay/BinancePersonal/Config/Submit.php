<?php
declare(strict_types=1);

return [
    [
        'title' => 'API Key',
        'name' => 'api_key',
        'type' => 'input',
        'placeholder' => '币安「API 管理」里新建，只勾选「允许读取」，并限制只允许服务器 IP 访问',
    ],
    [
        'title' => 'Secret Key',
        'name' => 'api_secret',
        'type' => 'password',
        'placeholder' => '创建 API Key 时显示的 Secret Key',
    ],
    [
        'title' => '汇率',
        'name' => 'rate',
        'type' => 'input',
        'placeholder' => '1 USDT = 多少元，例如 7.20。订单人民币金额除以它、向上取到分，就是买家要付的 USDT',
    ],
    [
        'title' => '付款时限（分钟）',
        'name' => 'timeout',
        'type' => 'input',
        'default' => '20',
        'placeholder' => '默认 20 分钟。超时后还会多等 30 分钟到账，这段时间里这个金额不会分给别的订单',
    ],
    [
        'title' => '币安 Pay ID',
        'name' => 'pay_id',
        'type' => 'input',
        'placeholder' => '用「币安 Pay 转账」时必填：币安 App → 支付 → 收款 页面上的 Pay ID',
    ],
    [
        'title' => '接口地址',
        'name' => 'api_base',
        'type' => 'input',
        'placeholder' => '留空即 https://api.binance.com；服务器连不上时可换 api1 / api2 / api3.binance.com',
    ],
];
