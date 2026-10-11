<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

use App\Entity\PayEntity;
use App\Pay\Pay;

/**
 * 把「网关下单结果」包装成 acg-faka 认得的 PayEntity。
 *
 * 三种呈现方式的取舍：
 * - 跳转（TYPE_REDIRECT）：URL 会写进订单的 pay_url。余额充值表的 pay_url 只有 255 字符，
 *   支付宝/易支付那种带签名的长 URL 会写不进去，所以长链接一律走表单提交。
 * - 表单提交（TYPE_SUBMIT）：pay_url 只存网关地址，参数放进订单附加数据，由系统模板 POST 过去。
 * - 本站收银台（TYPE_LOCAL_RENDER）：展示二维码或中转跳转，模板是插件 View/{code}.html，
 *   统一 include PayKit/View/Checkout.html；加密货币收款 include PayKit/View/Crypto.html（见 crypto()）。
 */
final class Checkout
{
    public static function redirect(string $url): PayEntity
    {
        $entity = new PayEntity();
        $entity->setType(Pay::TYPE_REDIRECT);
        $entity->setUrl($url);
        return $entity;
    }

    public static function submit(string $action, array $fields): PayEntity
    {
        $entity = new PayEntity();
        $entity->setType(Pay::TYPE_SUBMIT);
        $entity->setUrl($action);
        $entity->setOption(array_map(fn($v) => (string)$v, $fields));
        return $entity;
    }

    /**
     * 本站收银台展示二维码
     *
     * @param string $content 二维码内容（支付宝 qr_code、微信 code_url、易支付 qrcode）
     * @param string $brand alipay | wechat | qq | bank，决定图标和提示语
     * @param string $returnUrl 支付成功后跳回的地址
     * @param string $openUrl 手机上可直接唤起支付 App 的链接（可空）
     * @param string $queryUrl 支持主动查单的插件传本单的回调地址，买家停在收银台时页面会定时让本站去平台查单（见 ActiveQuery）
     */
    public static function qrcode(string $content, string $brand, string $returnUrl, string $openUrl = '', string $queryUrl = ''): PayEntity
    {
        return self::local($content, [
            'mode' => 'qrcode',
            'content' => $content,
            'brand' => $brand,
            'open_url' => $openUrl,
            'return_url' => $returnUrl,
            'query_url' => self::queryPath($queryUrl),
        ]);
    }

    /**
     * 本站收银台中转跳转（URL 太长存不进 pay_url，或需要从本站域名发起跳转带上 Referer，如微信 H5）
     */
    public static function jump(string $url, string $brand, string $returnUrl, string $queryUrl = ''): PayEntity
    {
        return self::local(mb_substr($url, 0, 255), [
            'mode' => 'jump',
            'content' => $url,
            'brand' => $brand,
            'open_url' => '',
            'return_url' => $returnUrl,
            'query_url' => self::queryPath($queryUrl),
        ]);
    }

    /**
     * 只留路径：回调域名可能和买家访问的域名不同，页面用同源路径请求，免得跨域
     */
    public static function queryPath(string $callbackUrl): string
    {
        $path = (string)parse_url($callbackUrl, PHP_URL_PATH);
        return preg_match('#^/user/api/(order|rechargeNotification)/callback\.\d+$#D', $path) === 1 ? $path : '';
    }

    /**
     * 加密货币网络：展示名 + 原生币（「请勿发送 BNB 或其他资产」这句要用）
     */
    public const CHAINS = [
        'tron' => ['TRON (TRC20)', 'TRX'],
        'bsc' => ['BSC (BEP20)', 'BNB'],
        'ethereum' => ['Ethereum (ERC20)', 'ETH'],
        'polygon' => ['Polygon', 'POL'],
        'arbitrum' => ['Arbitrum One', 'ETH'],
        'solana' => ['Solana', 'SOL'],
        'aptos' => ['Aptos', 'APT'],
        'ton' => ['TON', 'TON'],
    ];

    /**
     * 本站加密货币收银台（PayKit/View/Crypto.html）：金额、收款地址或 Pay ID、二维码、倒计时，以及转账说明和提醒
     *
     * @param string $mode chain 链上转账 | pay 币安 Pay | cashier 先看说明、再去网关收银台选网络
     * @param string $asset 币种，如 USDT
     * @param string $chain CHAINS 的键；pay、cashier 模式可空
     * @param string $target 收款地址或 Pay ID；cashier 模式可空
     * @param string $amount 要付的币数，原样展示（不要转 float，免得丢掉尾数）
     * @param array $extra label、cny、rate、expires（时间戳）、cashier_url、return_url、query_url（回调地址，主动查单用）
     */
    public static function crypto(string $mode, string $asset, string $chain, string $target, string $amount, array $extra): PayEntity
    {
        [$network, $native] = self::CHAINS[$chain] ?? ['', ''];
        $option = array_merge([
            'label' => $asset . ($network !== '' ? ' · ' . $network : ''),
            'cny' => '',
            'rate' => '',
            'expires' => '',
            'cashier_url' => '',
            'return_url' => '',
        ], $extra, [
            'mode' => $mode,
            'asset' => $asset,
            'network' => $network,
            'native' => $native,
            'target' => $target,
            'amount' => $amount,
            'query_url' => self::queryPath((string)($extra['query_url'] ?? '')),
        ]);
        return self::local($target !== '' ? $target : (string)$option['cashier_url'], $option);
    }

    private static function local(string $url, array $option): PayEntity
    {
        $entity = new PayEntity();
        $entity->setType(Pay::TYPE_LOCAL_RENDER);
        $entity->setUrl(mb_substr($url, 0, 255));
        $entity->setOption($option);
        return $entity;
    }
}
