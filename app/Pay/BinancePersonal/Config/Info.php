<?php
declare(strict_types=1);

/**
 * 币安收款（个人账户）
 *
 * 个人账户没有支付通知可收，靠「唯一金额 + 轮询到账记录」对账：
 * 每笔单在同一收款通道里分到一个独一无二的 USDT 金额（按分顺延），买家照这个金额转账，
 * 本站用只读 API Key 查币安的充值记录（链上充值到币安地址）或 Pay 交易记录（币安 Pay 转账），金额、通道、时间都对上才算这笔单已付款。
 * 查询由本站收银台页面和 PayKit/cron.php 定时任务触发，走 PayKit\ActiveQuery。
 */
return [
    'name' => '币安收款（个人账户）',
    'version' => '1.0.0',
    'author' => 'aisubproxy',
    'description' => '用个人币安账户收 USDT：链上充值到你的币安地址或币安 Pay 转账，按唯一金额自动对账',
    'options' => [
        'trc20' => 'USDT-TRC20',
        'bep20' => 'USDT-BEP20',
        'solana' => 'USDT-Solana',
        'arbitrum' => 'USDT-Arbitrum',
        'erc20' => 'USDT-ERC20',
        'pay' => '币安 Pay 转账',
    ],
    'callback' => [
        \App\Consts\Pay::IS_SIGN => true,
        \App\Consts\Pay::IS_STATUS => true,
        \App\Consts\Pay::FIELD_STATUS_KEY => 'status',
        \App\Consts\Pay::FIELD_STATUS_VALUE => 'paid',
        \App\Consts\Pay::FIELD_ORDER_KEY => 'order_id',
        \App\Consts\Pay::FIELD_AMOUNT_KEY => 'amount',
        \App\Consts\Pay::FIELD_RESPONSE => 'success',
    ],
];
