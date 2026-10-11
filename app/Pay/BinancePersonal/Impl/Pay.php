<?php
declare(strict_types=1);

namespace App\Pay\BinancePersonal\Impl;

use App\Entity\PayEntity;
use App\Pay\Base;
use App\Pay\PayKit\Checkout;
use Kernel\Exception\JSONException;

class Pay extends Base implements \App\Pay\Pay
{
    /**
     * 分一个唯一的 USDT 金额，在本站收银台展示收款地址（或 Pay ID）和金额，之后靠查单对账
     *
     * @return PayEntity
     * @throws JSONException
     */
    public function trade(): PayEntity
    {
        $rate = Rate::resolve($this->config);
        $minutes = (int)($this->config['timeout'] ?? 0);
        $timeout = ($minutes > 0 ? min($minutes, 180) : 20) * 60;

        if ($this->code === 'pay') {
            $target = trim((string)($this->config['pay_id'] ?? ''));
            if ($target === '') {
                throw new JSONException('币安 Pay ID 未配置');
            }
            [$channel, $label] = [Binance::PAY, '币安 Pay'];
        } elseif (isset(Binance::NETWORKS[$this->code])) {
            [$channel, $label] = Binance::NETWORKS[$this->code];
            $target = Binance::depositAddress($this->config, $channel);
        } else {
            throw new JSONException("币安收款不支持该支付方式：{$this->code}");
        }

        //人民币 ÷ 汇率，向上取到分：宁可多收几厘，不能少收。先 round 掉浮点误差，免得 7.2 / 7.2 算成 1.0000000001 被进到 1.01
        $baseCents = (int)ceil(round($this->amount / $rate * 100, 6));
        $cny = number_format($this->amount, 2, '.', '');
        $entry = Ledger::allocate($this->tradeNo, $channel, $baseCents, $cny, $timeout);
        $usdt = number_format($entry['cents'] / 100, 2, '.', '');
        $this->log("{$this->tradeNo} {$label} ¥{$cny} → {$usdt} USDT");

        $entity = new PayEntity();
        $entity->setType(\App\Pay\Pay::TYPE_LOCAL_RENDER);
        $entity->setUrl(mb_substr($target, 0, 255));
        $entity->setOption([
            'mode' => $channel === Binance::PAY ? 'pay' : 'chain',
            'label' => $label,
            'target' => $target,
            'usdt' => $usdt,
            'cny' => $cny,
            'rate' => rtrim(rtrim(number_format($rate, 4, '.', ''), '0'), '.'),
            'expires' => $entry['expires'],
            'return_url' => $this->returnUrl,
            'query_url' => Checkout::queryPath($this->callbackUrl),
        ]);
        return $entity;
    }
}
