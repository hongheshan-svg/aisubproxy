<?php
declare(strict_types=1);

return [
    [
        'title' => '网关地址',
        'name' => 'url',
        'type' => 'input',
        'placeholder' => '易支付平台的接口地址，例如 https://pay.example.com/（不要带 submit.php）',
    ],
    [
        'title' => '商户ID',
        'name' => 'pid',
        'type' => 'input',
        'placeholder' => '平台分配的商户ID（pid）',
    ],
    [
        'title' => '商户密钥',
        'name' => 'key',
        'type' => 'password',
        'placeholder' => '平台分配的 MD5 密钥（key）',
    ],
    [
        'title' => '下单方式',
        'name' => 'mode',
        'type' => 'select',
        'default' => 'submit',
        'dict' => [
            ['id' => 'submit', 'name' => '跳转收银台（submit.php，兼容性最好）'],
            ['id' => 'mapi', 'name' => '接口下单（mapi.php，在本站展示二维码）'],
        ],
    ],
];
