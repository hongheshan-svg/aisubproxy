<?php
declare(strict_types=1);

namespace App\Pay\PayKit;

/**
 * 「参数排序拼接 + 摘要」类签名：易支付用它加 MD5，支付宝用它拼 RSA2 的待签名串，只是排除字段不同。
 *
 * 本目录不是支付插件（没有 Config/Info.php，后台不会列出），只给同仓库的支付插件共用。
 */
final class Sign
{
    /**
     * 去掉排除字段与空值，按参数名 ASCII 升序拼成 k=v&k=v（值不做 URL 编码）。
     *
     * @param array $params
     * @param string[] $exclude
     * @return string
     */
    public static function canonical(array $params, array $exclude): string
    {
        ksort($params, SORT_STRING);
        $pairs = [];
        foreach ($params as $key => $value) {
            $key = (string)$key;
            if (in_array($key, $exclude, true) || $value === null || $value === '' || is_array($value)) {
                continue;
            }
            $pairs[] = $key . '=' . self::scalar($value);
        }
        return implode('&', $pairs);
    }

    /**
     * 标量转字符串；数字取最短写法（100 而不是 100.0），布尔值写成 true / false
     */
    public static function scalar(mixed $value): string
    {
        if (is_bool($value)) {
            return $value ? 'true' : 'false';
        }
        if (is_int($value)) {
            return (string)$value;
        }
        if (is_float($value)) {
            if (is_finite($value) && floor($value) == $value && abs($value) < 1e15) {
                return number_format($value, 0, '.', '');
            }
            //serialize_precision = -1 时 var_export 给出最短可往返表示，与 Go 的 -1 精度一致
            return var_export($value, true);
        }
        return (string)$value;
    }

    public static function md5(array $params, string $key, array $exclude): string
    {
        return md5(self::canonical($params, $exclude) . $key);
    }

    /**
     * 大小写不敏感、定长时间的签名比较
     */
    public static function equals(string $expected, mixed $actual): bool
    {
        return is_string($actual) && $actual !== '' && hash_equals(strtolower($expected), strtolower(trim($actual)));
    }
}
