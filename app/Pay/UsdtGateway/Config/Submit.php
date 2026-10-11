<?php
declare(strict_types=1);

return [
    [
        'title' => '网关类型',
        'name' => 'protocol',
        'type' => 'select',
        'default' => 'epusdt',
        'dict' => [
            ['id' => 'epusdt', 'name' => 'BEpusdt / 经典 epusdt（MD5 签名）'],
            ['id' => 'gmpay', 'name' => 'GM Pay 新版 epusdt（HMAC-SHA256 签名）'],
        ],
    ],
    [
        'title' => '网关地址',
        'name' => 'url',
        'type' => 'input',
        'placeholder' => '网关根地址，例如 https://usdt.example.com（也可直接填完整的 create-transaction 接口地址）',
    ],
    [
        'title' => 'API Token',
        'name' => 'token',
        'type' => 'password',
        'placeholder' => 'BEpusdt：后台「API 设置 → 对接令牌」；GM Pay：API Key 的 secret_key',
    ],
    [
        'title' => 'PID',
        'name' => 'pid',
        'type' => 'input',
        'placeholder' => '仅 GM Pay 需要：API Key 对应的 pid',
    ],
    [
        'title' => '订单超时（秒）',
        'name' => 'timeout',
        'type' => 'input',
        'placeholder' => '可空，留空使用网关默认值（BEpusdt 最低 120 秒）',
    ],
    [
        'title' => '付款页面',
        'name' => 'checkout',
        'type' => 'select',
        'default' => 'local',
        'dict' => [
            ['id' => 'local', 'name' => '本站收银台（展示地址、金额和转账说明，推荐）'],
            ['id' => 'gateway', 'name' => '直接跳转网关收银台'],
        ],
    ],
    [
        'title' => '汇率',
        'name' => 'rate',
        'type' => 'input',
        'placeholder' => '可空，仅 BEpusdt：7.2 固定汇率；~1.02 最新汇率上浮 2%；留空用网关配置',
    ],
];
