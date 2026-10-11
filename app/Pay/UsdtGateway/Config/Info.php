<?php
declare(strict_types=1);

/**
 * 收U：对接自建加密货币收款网关
 *
 * - BEpusdt（v03413/BEpusdt）与经典 epusdt：POST /api/v1/order/create-transaction，MD5(排序参数 + API Token)
 * - GM Pay（GMWalletApp/epusdt 新版）：POST /payments/gmpay/v1/order/create-transaction，HMAC-SHA256 + PID
 * 回调里的 amount 是法币（人民币）金额，直接与订单金额核对。
 */
return [
    'name' => '收U（USDT/USDC）',
    'version' => '1.0.0',
    'author' => 'aisubproxy',
    'description' => '对接 BEpusdt / epusdt / GM Pay 收款网关，多链 USDT、USDC',
    'options' => [
        'usdt.trc20' => 'USDT-TRC20',
        'usdt.polygon' => 'USDT-Polygon',
        'usdt.bep20' => 'USDT-BEP20',
        'usdt.erc20' => 'USDT-ERC20',
        'usdt.solana' => 'USDT-Solana',
        'usdt.aptos' => 'USDT-Aptos',
        'usdc.polygon' => 'USDC-Polygon',
        'usdc.bep20' => 'USDC-BEP20',
        'tron.trx' => 'TRX',
        'cashier' => '收银台自选币种',
    ],
    'callback' => [
        \App\Consts\Pay::IS_SIGN => true,
        \App\Consts\Pay::IS_STATUS => true,
        \App\Consts\Pay::FIELD_STATUS_KEY => 'status',
        \App\Consts\Pay::FIELD_STATUS_VALUE => '2',
        \App\Consts\Pay::FIELD_ORDER_KEY => 'order_id',
        \App\Consts\Pay::FIELD_AMOUNT_KEY => 'amount',
        \App\Consts\Pay::FIELD_RESPONSE => 'success',
    ],
];
