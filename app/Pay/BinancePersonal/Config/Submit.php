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
        'placeholder' => '留空=自动取实时价；7.20=固定汇率；~1.02=实时价×1.02；+0.05 / -0.05=实时价加减',
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
        'title' => '汇率接口',
        'name' => 'rate_api',
        'type' => 'input',
        'placeholder' => '一般留空，即 CoinGecko 的 USDT 人民币价格；服务器连不上 CoinGecko 时可换成返回同样格式的地址',
    ],
    [
        'title' => '接口地址',
        'name' => 'api_base',
        'type' => 'input',
        'placeholder' => '留空即 https://api.binance.com；服务器连不上时可换 api1 / api2 / api3.binance.com',
    ],
];
