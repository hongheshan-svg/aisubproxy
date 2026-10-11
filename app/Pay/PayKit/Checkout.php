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
 *   统一 include PayKit/View/Checkout.html。
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
     * @param string $brand alipay | wechat | qq | bank | usdt，决定图标和提示语
     * @param string $returnUrl 支付成功后跳回的地址
     * @param string $openUrl 手机上可直接唤起支付 App 的链接（可空）
     */
    public static function qrcode(string $content, string $brand, string $returnUrl, string $openUrl = ''): PayEntity
    {
        return self::local($content, [
            'mode' => 'qrcode',
            'content' => $content,
            'brand' => $brand,
            'open_url' => $openUrl,
            'return_url' => $returnUrl,
        ]);
    }

    /**
     * 本站收银台中转跳转（URL 太长存不进 pay_url，或需要从本站域名发起跳转带上 Referer，如微信 H5）
     */
    public static function jump(string $url, string $brand, string $returnUrl): PayEntity
    {
        return self::local(mb_substr($url, 0, 255), [
            'mode' => 'jump',
            'content' => $url,
            'brand' => $brand,
            'open_url' => '',
            'return_url' => $returnUrl,
        ]);
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
