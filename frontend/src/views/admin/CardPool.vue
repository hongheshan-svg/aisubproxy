<template>
  <div class="space-y-4">
    <div class="card">
      <h2 class="text-xl font-bold text-ink">卡池</h2>
      <p class="text-sm text-muted mt-1">MarsWallex 卡池：库存、卡、持卡人、单据、交易与策略</p>
    </div>

    <div class="card">
      <el-tabs v-model="tab">
        <!-- ============ 概览 ============ -->
        <el-tab-pane label="概览" name="overview">
          <div class="flex justify-end mb-2">
            <el-button size="small" :loading="ovLoading" @click="loadOverview">刷新</el-button>
          </div>
          <div v-if="overview" class="space-y-4">
            <div>
              <div class="text-xs uppercase tracking-wide text-muted mb-1">主钱包余额</div>
              <p v-if="overview.balance_error" class="text-sm text-warn">{{ overview.balance_error }}</p>
              <el-table v-else :data="overview.main_balances" size="small" border empty-text="暂无余额">
                <el-table-column prop="currency" label="币种" width="100" />
                <el-table-column prop="available_balance" label="可用" min-width="120" />
                <el-table-column prop="freeze_balance" label="冻结" min-width="120" />
              </el-table>
            </div>

            <div class="flex flex-wrap gap-3">
              <button type="button" class="pending-chip" @click="goPending('orders')">
                待复核单据 <b>{{ overview.needs_review }}</b>
              </button>
              <button type="button" class="pending-chip" @click="goPending('events')">
                失败的 Webhook 事件 <b>{{ overview.failed_events }}</b>
              </button>
              <button type="button" class="pending-chip" @click="goPending('policy')">
                已暂停的产品 <b>{{ overview.suspended_products }}</b>
              </button>
            </div>

            <div>
              <div class="text-xs uppercase tracking-wide text-muted mb-1">产品库存</div>
              <el-table :data="overview.products" size="small" border empty-text="暂无库存">
                <el-table-column label="产品" min-width="160">
                  <template #default="{ row }">{{ productLabel(row.product_id) }}</template>
                </el-table-column>
                <el-table-column prop="available" label="可用" width="70" />
                <el-table-column prop="leased" label="租用中" width="80" />
                <el-table-column prop="releasing" label="清零中" width="80" />
                <el-table-column prop="used" label="已用" width="70" />
                <el-table-column prop="disabled" label="停用" width="70" />
                <el-table-column prop="target" label="目标" width="70" />
                <el-table-column prop="opened_today" label="今日开卡" width="90" />
                <el-table-column prop="daily_limit" label="上限" width="70" />
              </el-table>
            </div>
          </div>
        </el-tab-pane>

        <!-- ============ 卡 ============ -->
        <el-tab-pane label="卡" name="cards">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-select v-model="cardFilter.state" clearable placeholder="状态" style="width: 130px" @change="resetCards">
              <el-option v-for="s in POOL_STATES" :key="s.value" :label="s.label" :value="s.value" />
            </el-select>
            <el-select v-model="cardFilter.product_id" clearable filterable placeholder="产品" style="width: 200px" @change="resetCards">
              <el-option v-for="p in products" :key="p.product_id" :label="productLabel(p.product_id)" :value="p.product_id" />
            </el-select>
            <el-button size="small" :loading="cardsLoading" @click="loadCards">刷新</el-button>
            <div class="flex-1" />
            <el-button type="primary" size="small" @click="openApply">手动开卡</el-button>
            <el-button size="small" @click="openImportCard">导入卡</el-button>
          </div>
          <el-table :data="cards" size="small" border empty-text="暂无卡" v-loading="cardsLoading">
            <el-table-column prop="card_id" label="card_id" min-width="170" show-overflow-tooltip />
            <el-table-column prop="last4" label="后四位" width="80" />
            <el-table-column label="产品" min-width="130">
              <template #default="{ row }">{{ productLabel(row.product_id) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="stateTagType(row.pool_state)">{{ poolStateLabel(row.pool_state) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="platform_status" label="平台状态" width="100" />
            <el-table-column label="余额" width="90">
              <template #default="{ row }">{{ money(row.available_cents) }}</template>
            </el-table-column>
            <el-table-column prop="use_count" label="已用次数" width="80" />
            <el-table-column prop="expiry" label="到期" width="80" />
            <el-table-column label="租用" min-width="140">
              <template #default="{ row }">
                <span v-if="row.pool_state === 'leased'" class="text-xs text-subtle">
                  {{ row.lease_owner }}<br />至 {{ fmtTs(row.lease_expires_at) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column label="操作" min-width="360" fixed="right">
              <template #default="{ row }">
                <div class="flex flex-wrap gap-1">
                  <el-button size="small" text @click="openDetail(row)">详情</el-button>
                  <template v-if="row.pool_state === 'available'">
                    <el-button size="small" type="primary" plain @click="leaseCard(row)">租用</el-button>
                    <el-button size="small" type="warning" plain @click="disableCard(row)">停用</el-button>
                  </template>
                  <template v-if="isMine(row)">
                    <el-button size="small" type="primary" plain @click="fundCard(row)">注资</el-button>
                    <el-button size="small" type="primary" plain @click="viewSensitive(row)">查看卡号</el-button>
                    <el-button size="small" type="primary" plain @click="viewOtp(row)">查看验证码</el-button>
                    <el-button size="small" plain @click="releaseCard(row)">释放</el-button>
                  </template>
                  <el-button v-if="row.pool_state === 'disabled'" size="small" type="success" plain @click="enableCard(row)">启用</el-button>
                  <template v-if="row.pool_state !== 'retired'">
                    <el-button size="small" plain @click="simpleAction(row, 'refresh', '已刷新')">刷新</el-button>
                    <el-button size="small" plain @click="simpleAction(row, 'freeze', '已提交冻结')">冻结</el-button>
                    <el-button size="small" plain @click="simpleAction(row, 'unfreeze', '已提交解冻')">解冻</el-button>
                    <el-button size="small" plain @click="sweepCard(row)">清零</el-button>
                    <el-button size="small" type="danger" plain @click="cancelCard(row)">注销</el-button>
                  </template>
                </div>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination
            class="mt-3"
            layout="total, prev, pager, next"
            :total="cardTotal"
            :page-size="PAGE_SIZE"
            v-model:current-page="cardFilter.page"
            @current-change="loadCards"
          />
        </el-tab-pane>

        <!-- ============ 持卡人 ============ -->
        <el-tab-pane label="持卡人" name="holders">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-input v-model="importHolderId" placeholder="持卡人 ID" style="width: 260px" class="mono" />
            <el-button type="primary" size="small" :loading="holderImporting" @click="importHolder">按 ID 导入</el-button>
            <el-button size="small" :loading="holdersLoading" @click="loadHolders">刷新列表</el-button>
          </div>
          <el-table :data="holders" size="small" border empty-text="暂无持卡人" v-loading="holdersLoading">
            <el-table-column prop="card_holder_id" label="持卡人 ID" min-width="190" show-overflow-tooltip />
            <el-table-column label="姓名" min-width="140">
              <template #default="{ row }">{{ row.first_name }} {{ row.last_name }}</template>
            </el-table-column>
            <el-table-column prop="country_code" label="国家" width="80" />
            <el-table-column prop="status" label="状态" width="100" />
            <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
            <el-table-column label="同步时间" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.synced_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="90">
              <template #default="{ row }">
                <el-button size="small" plain @click="refreshHolder(row)">刷新</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <!-- ============ 单据 ============ -->
        <el-tab-pane label="单据" name="orders">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-select v-model="orderFilter.state" clearable placeholder="状态" style="width: 140px" @change="resetOrders">
              <el-option v-for="s in ORDER_STATES" :key="s" :label="s" :value="s" />
            </el-select>
            <el-select v-model="orderFilter.kind" clearable placeholder="类型" style="width: 130px" @change="resetOrders">
              <el-option v-for="k in ORDER_KINDS" :key="k.value" :label="k.label" :value="k.value" />
            </el-select>
            <el-button size="small" :loading="ordersLoading" @click="loadOrders">刷新</el-button>
          </div>
          <el-table :data="orders" size="small" border empty-text="暂无单据" v-loading="ordersLoading">
            <el-table-column prop="out_order_no" label="单号" min-width="200" show-overflow-tooltip />
            <el-table-column label="类型" width="80">
              <template #default="{ row }">{{ kindLabel(row.kind) }}</template>
            </el-table-column>
            <el-table-column prop="card_id" label="card_id" min-width="150" show-overflow-tooltip />
            <el-table-column label="金额" width="90">
              <template #default="{ row }">{{ money(row.amount_cents) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row }">
                <el-tag size="small" :type="orderTagType(row.state)">{{ row.state }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="attempts" label="次数" width="60" />
            <el-table-column prop="last_error" label="错误" min-width="160" show-overflow-tooltip />
            <el-table-column prop="note" label="备注" min-width="120" show-overflow-tooltip />
            <el-table-column label="创建" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.created_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="170" fixed="right">
              <template #default="{ row }">
                <template v-if="row.state === 'needs_review'">
                  <el-button size="small" plain @click="recheckOrder(row)">再查一次</el-button>
                  <el-button size="small" type="danger" plain @click="confirmFailed(row)">确认失败</el-button>
                </template>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination
            class="mt-3"
            layout="total, prev, pager, next"
            :total="orderTotal"
            :page-size="PAGE_SIZE"
            v-model:current-page="orderFilter.page"
            @current-change="loadOrders"
          />
        </el-tab-pane>

        <!-- ============ 交易 ============ -->
        <el-tab-pane label="交易" name="txns">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-input v-model="txnFilter.card_id" clearable placeholder="按 card_id 筛选" style="width: 260px" class="mono" @change="resetTxns" />
            <el-button size="small" :loading="txnsLoading" @click="loadTxns">刷新</el-button>
          </div>
          <el-table :data="txns" size="small" border empty-text="暂无交易" v-loading="txnsLoading">
            <el-table-column prop="transaction_id" label="交易 ID" min-width="190" show-overflow-tooltip />
            <el-table-column prop="kind" label="类型" width="100" />
            <el-table-column prop="card_id" label="card_id" min-width="150" show-overflow-tooltip />
            <el-table-column prop="status" label="状态" width="100" />
            <el-table-column label="金额" min-width="120">
              <template #default="{ row }">{{ row.txn_amount }} {{ row.txn_currency }}</template>
            </el-table-column>
            <el-table-column label="入账金额" min-width="120">
              <template #default="{ row }">{{ row.billing_amount }} {{ row.billing_currency }}</template>
            </el-table-column>
            <el-table-column prop="merchant_name" label="商户" min-width="140" show-overflow-tooltip />
            <el-table-column prop="decline_reason" label="拒绝原因" min-width="140" show-overflow-tooltip />
            <el-table-column label="时间" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.occurred_at) }}</template>
            </el-table-column>
          </el-table>
          <el-pagination
            class="mt-3"
            layout="total, prev, pager, next"
            :total="txnTotal"
            :page-size="PAGE_SIZE"
            v-model:current-page="txnFilter.page"
            @current-change="loadTxns"
          />
        </el-tab-pane>

        <!-- ============ 策略与健康度 ============ -->
        <el-tab-pane label="策略与健康度" name="policy">
          <div v-if="policy" class="space-y-4">
            <el-form label-position="top" class="policy-form" @submit.prevent>
              <div class="grid-form">
                <el-form-item label="开卡金额（元）">
                  <el-input v-model="openAmountText" />
                </el-form-item>
                <el-form-item label="有效期（月，2-47）">
                  <el-input-number v-model="policy.validity_months" :min="2" :max="47" />
                </el-form-item>
                <el-form-item label="卡名前缀（1-10 位字母数字）">
                  <el-input v-model="policy.card_name_prefix" maxlength="10" />
                </el-form-item>
                <el-form-item label="单卡最大使用次数（1-100）">
                  <el-input-number v-model="policy.max_uses_per_card" :min="1" :max="100" />
                </el-form-item>
                <el-form-item label="租期（分钟，5-240）">
                  <el-input-number v-model="policy.lease_minutes" :min="5" :max="240" />
                </el-form-item>
                <el-form-item label="用完后注销等待（天，0-90）">
                  <el-input-number v-model="policy.cancel_after_days" :min="0" :max="90" />
                </el-form-item>
                <el-form-item label="健康度统计窗口（小时，1-720）">
                  <el-input-number v-model="policy.health_window_hours" :min="1" :max="720" />
                </el-form-item>
                <el-form-item label="产品失败阈值（1-100）">
                  <el-input-number v-model="policy.product_fail_threshold" :min="1" :max="100" />
                </el-form-item>
                <el-form-item label="单卡失败阈值（1-100）">
                  <el-input-number v-model="policy.card_fail_threshold" :min="1" :max="100" />
                </el-form-item>
                <el-form-item label="自动暂停">
                  <el-switch v-model="policy.auto_suspend" />
                </el-form-item>
              </div>

              <el-form-item label="开卡持卡人（可多选）">
                <el-select v-model="policy.holder_ids" multiple filterable allow-create default-first-option style="width: 100%" placeholder="选择或输入持卡人 ID">
                  <el-option v-for="h in holders" :key="h.card_holder_id" :value="h.card_holder_id"
                    :label="`${h.card_holder_id}（${h.first_name} ${h.last_name}）`" />
                </el-select>
              </el-form-item>

              <div class="text-sm font-semibold text-ink mb-1">各套餐的产品优先级（自上而下）</div>
              <div v-for="key in planKeys" :key="key" class="plan-box">
                <div class="flex items-center gap-2 mb-2">
                  <b class="mono">{{ key }}</b>
                  <span v-if="key === 'default'" class="text-xs text-subtle">兜底</span>
                  <el-button v-if="key !== 'default'" size="small" text type="danger" @click="removePlan(key)">删除套餐</el-button>
                </div>
                <div v-for="(pid, i) in policy.plan_products[key]" :key="pid" class="flex items-center gap-2 mb-1">
                  <span class="text-xs text-muted w-5">{{ i + 1 }}.</span>
                  <span class="flex-1 text-sm">{{ productLabel(pid) }}</span>
                  <el-button size="small" text :disabled="i === 0" @click="moveProduct(key, i, -1)">上移</el-button>
                  <el-button size="small" text :disabled="i === policy.plan_products[key].length - 1" @click="moveProduct(key, i, 1)">下移</el-button>
                  <el-button size="small" text type="danger" @click="policy.plan_products[key].splice(i, 1)">移除</el-button>
                </div>
                <el-select :model-value="''" placeholder="添加产品" style="width: 260px" size="small" @change="(v: string) => addProduct(key, v)">
                  <el-option v-for="p in products.filter((x) => !policy!.plan_products[key].includes(x.product_id))"
                    :key="p.product_id" :label="productLabel(p.product_id)" :value="p.product_id" />
                </el-select>
              </div>
              <div class="flex items-center gap-2 mt-2 mb-4">
                <el-input v-model="newPlanKey" size="small" placeholder="新增套餐 key" style="width: 200px" class="mono" />
                <el-button size="small" @click="addPlan">添加套餐</el-button>
              </div>

              <div class="text-sm font-semibold text-ink mb-1">各产品目标数</div>
              <el-table :data="targetRows" size="small" border empty-text="暂无产品" class="mb-2">
                <el-table-column label="产品" min-width="200">
                  <template #default="{ row }">{{ productLabel(row.id) }}</template>
                </el-table-column>
                <el-table-column label="目标库存（0-500）" width="180">
                  <template #default="{ row }">
                    <el-input-number v-model="row.target.target" :min="0" :max="500" size="small" />
                  </template>
                </el-table-column>
                <el-table-column label="每日开卡上限（0-500）" width="200">
                  <template #default="{ row }">
                    <el-input-number v-model="row.target.daily_limit" :min="0" :max="500" size="small" />
                  </template>
                </el-table-column>
              </el-table>
            </el-form>

            <p v-if="policyError" class="field-error">{{ policyError }}</p>
            <div class="flex gap-2">
              <el-button type="primary" :loading="policySaving" @click="savePolicy">保存策略</el-button>
              <el-button @click="loadPolicy">重新加载</el-button>
            </div>
          </div>

          <div class="mt-6">
            <div class="flex items-center justify-between mb-2">
              <h3 class="text-lg font-bold text-ink">健康度</h3>
              <el-button size="small" :loading="healthLoading" @click="loadHealth">刷新</el-button>
            </div>
            <el-table :data="health" size="small" border empty-text="暂无数据" v-loading="healthLoading">
              <el-table-column label="产品" min-width="180">
                <template #default="{ row }">{{ productLabel(row.product_id) }}</template>
              </el-table-column>
              <el-table-column prop="plan_key" label="套餐" width="110" />
              <el-table-column label="24h 成功/失败" width="130">
                <template #default="{ row }">{{ row.success_24h }} / {{ row.failed_24h }}</template>
              </el-table-column>
              <el-table-column label="7d 成功/失败" width="130">
                <template #default="{ row }">{{ row.success_7d }} / {{ row.failed_7d }}</template>
              </el-table-column>
              <el-table-column label="状态" width="90">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.suspended ? 'danger' : 'success'">{{ row.suspended ? '已暂停' : '正常' }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
              <el-table-column label="暂停时间" min-width="150">
                <template #default="{ row }">{{ fmtTs(row.suspended_at) }}</template>
              </el-table-column>
              <el-table-column label="操作" width="90">
                <template #default="{ row }">
                  <el-button v-if="row.suspended" size="small" type="success" plain @click="resumeProduct(row)">恢复</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>

    <!-- 手动开卡 -->
    <el-dialog v-model="applyOpen" title="手动开卡" width="460px" :close-on-click-modal="false">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="产品">
          <el-select v-model="applyForm.product_id" filterable style="width: 100%">
            <el-option v-for="p in products" :key="p.product_id" :label="productLabel(p.product_id)" :value="p.product_id" />
          </el-select>
        </el-form-item>
        <el-form-item label="持卡人">
          <el-select v-model="applyForm.card_holder_id" filterable style="width: 100%">
            <el-option v-for="h in holders" :key="h.card_holder_id" :value="h.card_holder_id"
              :label="`${h.card_holder_id}（${h.first_name} ${h.last_name}）`" />
          </el-select>
        </el-form-item>
        <el-form-item label="数量（1-20）">
          <el-input-number v-model="applyForm.count" :min="1" :max="20" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="applyOpen = false">取消</el-button>
        <el-button type="primary" :loading="applying" @click="submitApply">开卡</el-button>
      </template>
    </el-dialog>

    <!-- 导入卡 -->
    <el-dialog v-model="importOpen" title="导入卡" width="460px" :close-on-click-modal="false">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="card_id">
          <el-input v-model="importForm.card_id" class="mono" />
        </el-form-item>
        <el-form-item label="产品">
          <el-select v-model="importForm.product_id" filterable style="width: 100%">
            <el-option v-for="p in products" :key="p.product_id" :label="productLabel(p.product_id)" :value="p.product_id" />
          </el-select>
        </el-form-item>
        <el-form-item label="持卡人（可选）">
          <el-select v-model="importForm.card_holder_id" clearable filterable style="width: 100%">
            <el-option v-for="h in holders" :key="h.card_holder_id" :value="h.card_holder_id"
              :label="`${h.card_holder_id}（${h.first_name} ${h.last_name}）`" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importOpen = false">取消</el-button>
        <el-button type="primary" :loading="importing" @click="submitImportCard">导入</el-button>
      </template>
    </el-dialog>

    <!-- 卡详情 -->
    <el-drawer v-model="detailOpen" :title="`卡详情 ${detail?.card.card_id || ''}`" size="720px" @closed="detail = null">
      <div v-if="detail" class="space-y-4">
        <el-descriptions :column="2" size="small" border>
          <el-descriptions-item label="产品">{{ productLabel(detail.card.product_id) }}</el-descriptions-item>
          <el-descriptions-item label="后四位">{{ detail.card.last4 }}</el-descriptions-item>
          <el-descriptions-item label="池状态">{{ poolStateLabel(detail.card.pool_state) }}</el-descriptions-item>
          <el-descriptions-item label="平台状态">{{ detail.card.platform_status }}</el-descriptions-item>
          <el-descriptions-item label="可用余额">{{ money(detail.card.available_cents) }}</el-descriptions-item>
          <el-descriptions-item label="冻结余额">{{ money(detail.card.frozen_cents) }}</el-descriptions-item>
          <el-descriptions-item label="已用次数">{{ detail.card.use_count }}</el-descriptions-item>
          <el-descriptions-item label="到期">{{ detail.card.expiry }}</el-descriptions-item>
          <el-descriptions-item label="停用原因">{{ detail.card.disabled_reason || '—' }}</el-descriptions-item>
          <el-descriptions-item label="退役原因">{{ detail.card.retire_reason || '—' }}</el-descriptions-item>
        </el-descriptions>
        <div>
          <div class="text-sm font-semibold text-ink mb-1">单据</div>
          <el-table :data="detail.orders" size="small" border empty-text="暂无单据">
            <el-table-column prop="out_order_no" label="单号" min-width="190" show-overflow-tooltip />
            <el-table-column label="类型" width="70"><template #default="{ row }">{{ kindLabel(row.kind) }}</template></el-table-column>
            <el-table-column label="金额" width="80"><template #default="{ row }">{{ money(row.amount_cents) }}</template></el-table-column>
            <el-table-column prop="state" label="状态" width="110" />
            <el-table-column prop="last_error" label="错误" min-width="120" show-overflow-tooltip />
          </el-table>
        </div>
        <div>
          <div class="text-sm font-semibold text-ink mb-1">交易</div>
          <el-table :data="detail.transactions" size="small" border empty-text="暂无交易">
            <el-table-column prop="transaction_id" label="交易 ID" min-width="160" show-overflow-tooltip />
            <el-table-column prop="status" label="状态" width="90" />
            <el-table-column label="金额" min-width="100"><template #default="{ row }">{{ row.txn_amount }} {{ row.txn_currency }}</template></el-table-column>
            <el-table-column prop="merchant_name" label="商户" min-width="120" show-overflow-tooltip />
          </el-table>
        </div>
      </div>
    </el-drawer>

    <!-- 卡号弹窗：关闭即清空 -->
    <el-dialog v-model="sensitiveOpen" title="完整卡号" width="440px" destroy-on-close :close-on-click-modal="false" @closed="clearSensitive">
      <el-descriptions v-if="sensitive" :column="1" border size="small">
        <el-descriptions-item label="卡号"><span class="mono">{{ sensitive.card_no }}</span></el-descriptions-item>
        <el-descriptions-item label="CVV2"><span class="mono">{{ sensitive.cvv2 }}</span></el-descriptions-item>
        <el-descriptions-item label="有效期"><span class="mono">{{ sensitive.expiry_date }}</span></el-descriptions-item>
      </el-descriptions>
      <p class="text-xs text-subtle mt-2">关闭窗口后不再保留，请勿截图外传。</p>
      <template #footer><el-button type="primary" @click="sensitiveOpen = false">关闭</el-button></template>
    </el-dialog>

    <!-- 验证码弹窗：关闭即清空 -->
    <el-dialog v-model="otpOpen" title="3DS 验证码" width="440px" destroy-on-close :close-on-click-modal="false" @closed="clearOtp">
      <el-descriptions v-if="otp" :column="1" border size="small">
        <el-descriptions-item label="验证码"><span class="mono otp">{{ otp.otp }}</span></el-descriptions-item>
        <el-descriptions-item label="商户">{{ otp.merchant_name || '—' }}</el-descriptions-item>
        <el-descriptions-item label="金额">{{ otp.amount }} {{ otp.currency }}</el-descriptions-item>
        <el-descriptions-item label="收到时间">{{ fmtIso(otp.received_at) }}</el-descriptions-item>
      </el-descriptions>
      <template #footer><el-button type="primary" @click="otpOpen = false">关闭</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'
import { useAuthStore } from '../../stores/auth'

interface Product { product_id: string; product_name: string; card_label: string }
interface Stock {
  product_id: string; available: number; leased: number; releasing: number; used: number
  disabled: number; target: number; daily_limit: number; opened_today: number
}
interface Overview {
  main_balances: { currency: string; available_balance: string; freeze_balance: string }[]
  balance_error: string
  products: Stock[]
  needs_review: number
  failed_events: number
  suspended_products: number
}
interface Card {
  card_id: string; card_holder_id: string; product_id: string; last4: string; expiry: string
  platform_status: string; available_cents: number; frozen_cents: number; pool_state: string
  lease_owner: string; lease_expires_at: number | null; use_count: number
  disabled_reason: string; retire_reason: string
}
interface Holder {
  card_holder_id: string; first_name: string; last_name: string; country_code: string
  status: string; remark: string; synced_at: number | null
}
interface Order {
  out_order_no: string; kind: string; card_id: string; amount_cents: number; state: string
  attempts: number; last_error: string; note: string; created_at: number
}
interface Txn {
  transaction_id: string; kind: string; card_id: string; status: string; decline_reason: string
  txn_amount: string; txn_currency: string; billing_amount: string; billing_currency: string
  merchant_name: string; occurred_at: number | null
}
interface Policy {
  plan_products: Record<string, string[]>
  product_targets: Record<string, { target: number; daily_limit: number }>
  holder_ids: string[]
  open_amount_cents: number
  validity_months: number
  card_name_prefix: string
  max_uses_per_card: number
  lease_minutes: number
  cancel_after_days: number
  health_window_hours: number
  product_fail_threshold: number
  card_fail_threshold: number
  auto_suspend: boolean
}
interface Health {
  product_id: string; plan_key: string; success_24h: number; failed_24h: number
  success_7d: number; failed_7d: number; suspended: boolean; suspended_at: number | null; reason: string
}

const PAGE_SIZE = 20
const POOL_STATES = [
  { value: 'available', label: '可用' }, { value: 'leased', label: '租用中' },
  { value: 'releasing', label: '清零中' }, { value: 'used', label: '已用' },
  { value: 'retired', label: '已注销' }, { value: 'disabled', label: '停用' },
]
const ORDER_STATES = ['pending', 'processing', 'unknown', 'needs_review', 'success', 'failed', 'not_sent']
const ORDER_KINDS = [
  { value: 'apply', label: '开卡' }, { value: 'recharge', label: '转入' },
  { value: 'withdraw', label: '转出' }, { value: 'status', label: '改状态' },
]

const router = useRouter()
const auth = useAuthStore()
const tab = ref('overview')

// ---------- 通用 ----------
interface Resp<T = any> { ok: boolean; status: number; data: T }

async function api<T = any>(path: string, method = 'GET', body?: unknown): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin/cardpool' + path, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

function errText(r: Resp, fallback: string) {
  return (r.data && r.data.error) || fallback
}

function money(cents: number | null | undefined) {
  return ((cents || 0) / 100).toFixed(2)
}

function fmtTs(ts: number | null | undefined) {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

function fmtIso(s: string) {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString()
}

function poolStateLabel(s: string) {
  return POOL_STATES.find((x) => x.value === s)?.label || s
}
function kindLabel(k: string) {
  return ORDER_KINDS.find((x) => x.value === k)?.label || k
}
function stateTagType(s: string) {
  switch (s) {
    case 'available': return 'success'
    case 'leased': return 'warning'
    case 'disabled': case 'retired': return 'info'
    case 'releasing': return 'warning'
    default: return ''
  }
}
function orderTagType(s: string) {
  switch (s) {
    case 'success': return 'success'
    case 'failed': case 'not_sent': return 'info'
    case 'needs_review': return 'danger'
    case 'unknown': return 'warning'
    default: return ''
  }
}

const products = ref<Product[]>([])
function productLabel(id: string) {
  const p = products.value.find((x) => x.product_id === id)
  return p ? `${p.product_name || p.card_label || id}（${id}）` : id
}

function isMine(c: Card) {
  return c.pool_state === 'leased' && c.lease_owner === 'admin:' + auth.username
}

// ---------- 概览 ----------
const overview = ref<Overview | null>(null)
const ovLoading = ref(false)

async function loadOverview() {
  ovLoading.value = true
  try {
    const r = await api<Overview>('/overview')
    if (!r.ok) return dialog.toast(errText(r, '加载概览失败'), 'err')
    overview.value = r.data
  } finally {
    ovLoading.value = false
  }
}

function goPending(kind: 'orders' | 'events' | 'policy') {
  if (kind === 'events') {
    router.push('/ops/marswallex')
    return
  }
  if (kind === 'orders') {
    orderFilter.state = 'needs_review'
    orderFilter.kind = ''
    orderFilter.page = 1
  }
  tab.value = kind
}

async function loadProducts() {
  const r = await api<{ items: Product[] }>('/products')
  if (r.ok) products.value = r.data.items || []
  else dialog.toast(errText(r, '加载产品失败'), 'warn')
}

// ---------- 卡 ----------
const cards = ref<Card[]>([])
const cardTotal = ref(0)
const cardsLoading = ref(false)
const cardFilter = reactive({ state: '', product_id: '', page: 1 })

async function loadCards() {
  cardsLoading.value = true
  try {
    const q = new URLSearchParams({ page: String(cardFilter.page), page_size: String(PAGE_SIZE) })
    if (cardFilter.state) q.set('state', cardFilter.state)
    if (cardFilter.product_id) q.set('product_id', cardFilter.product_id)
    const r = await api<{ items: Card[]; total: number }>('/cards?' + q)
    if (!r.ok) return dialog.toast(errText(r, '加载卡失败'), 'err')
    cards.value = r.data.items
    cardTotal.value = r.data.total
  } finally {
    cardsLoading.value = false
  }
}
function resetCards() {
  cardFilter.page = 1
  loadCards()
}

// 通用：执行一个对卡的动作，成功后刷新列表
async function runCardAction(row: Card, action: string, ok: string, body?: unknown) {
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}/${action}`, 'POST', body)
  if (!r.ok) {
    dialog.toast(errText(r, '操作失败'), 'err')
    return false
  }
  dialog.toast(ok, 'ok')
  loadCards()
  return true
}

function simpleAction(row: Card, action: string, ok: string) {
  return runCardAction(row, action, ok)
}

async function leaseCard(row: Card) {
  await runCardAction(row, 'lease', '已租用')
}
async function releaseCard(row: Card) {
  await runCardAction(row, 'release', '已释放')
}
async function enableCard(row: Card) {
  await runCardAction(row, 'enable', '已启用')
}
async function disableCard(row: Card) {
  const reason = await dialog.prompt(`停用卡 ${row.card_id}（后四位 ${row.last4}）`, { title: '停用', placeholder: '原因（可选）' })
  if (reason === null || reason === undefined) return
  await runCardAction(row, 'disable', '已停用', { reason: String(reason).trim() })
}
async function sweepCard(row: Card) {
  await runCardAction(row, 'sweep', '已提交清零')
}
async function cancelCard(row: Card) {
  const ok = await dialog.confirm(`确认注销卡 ${row.card_id}（后四位 ${row.last4}）？注销后不可恢复。`, {
    title: '注销', okText: '注销', danger: true,
  })
  if (!ok) return
  await runCardAction(row, 'cancel', '已提交注销')
}

async function fundCard(row: Card) {
  const v = await dialog.prompt(`向卡 ${row.card_id}（后四位 ${row.last4}）注资，金额（最多两位小数）`, {
    title: '注资', placeholder: '例如 1.00',
  })
  if (v === null || v === undefined) return
  const amount = String(v).trim()
  if (!/^\d+(\.\d{1,2})?$/.test(amount) || Number(amount) <= 0) {
    return dialog.toast('金额无效，须大于 0 且最多两位小数', 'err')
  }
  await runCardAction(row, 'fund', '已提交注资', { amount })
}

// 敏感信息：只存在内存里，关闭弹窗即清空；不写日志、不进存储。
const sensitive = ref<{ card_no: string; cvv2: string; expiry_date: string } | null>(null)
const sensitiveOpen = ref(false)
const otp = ref<{ otp: string; merchant_name: string; amount: string; currency: string; received_at: string } | null>(null)
const otpOpen = ref(false)

function clearSensitive() {
  sensitive.value = null
}
function clearOtp() {
  otp.value = null
}

async function viewSensitive(row: Card) {
  const ok = await dialog.confirm(`将显示卡 ${row.card_id}（后四位 ${row.last4}）的完整卡号和 CVV2，此操作会被记录。继续？`, {
    title: '查看卡号', okText: '查看',
  })
  if (!ok) return
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}/sensitive`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '获取卡号失败'), 'err')
  sensitive.value = { card_no: r.data.card_no, cvv2: r.data.cvv2, expiry_date: r.data.expiry_date }
  sensitiveOpen.value = true
  loadCards()
}

async function viewOtp(row: Card) {
  const ok = await dialog.confirm(`将显示卡 ${row.card_id}（后四位 ${row.last4}）最近收到的 3DS 验证码，此操作会被记录。继续？`, {
    title: '查看验证码', okText: '查看',
  })
  if (!ok) return
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}/otp`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '获取验证码失败'), 'err')
  otp.value = {
    otp: r.data.otp, merchant_name: r.data.merchant_name, amount: r.data.amount,
    currency: r.data.currency, received_at: r.data.received_at,
  }
  otpOpen.value = true
}

// 手动开卡
const applyOpen = ref(false)
const applying = ref(false)
const applyForm = reactive({ product_id: '', card_holder_id: '', count: 1 })

async function openApply() {
  if (!holders.value.length) await loadHolders()
  applyOpen.value = true
}

async function submitApply() {
  if (!applyForm.product_id || !applyForm.card_holder_id) return dialog.toast('请选择产品和持卡人', 'warn')
  const ok = await dialog.confirm(
    `将开 ${applyForm.count} 张卡\n产品：${productLabel(applyForm.product_id)}\n持卡人：${applyForm.card_holder_id}\n开卡会产生费用，继续？`,
    { title: '确认开卡', okText: '确认开卡' },
  )
  if (!ok) return
  applying.value = true
  try {
    const r = await api<{ orders?: Order[]; created?: number }>('/cards/apply', 'POST', {
      product_id: applyForm.product_id, card_holder_id: applyForm.card_holder_id, count: applyForm.count,
    })
    const orderNos = (r.data.orders || []).map((o) => o.out_order_no)
    if (!r.ok) {
      if (orderNos.length) {
        // 部分已创建：必须让管理员看到，避免盲目重试多开卡。
        applyOpen.value = false
        await dialog.alert(
          `开卡中途失败：${errText(r, '未知错误')}\n\n已创建 ${r.data.created ?? orderNos.length} 张，单号：\n${orderNos.join('\n')}\n\n请先到「单据」标签核对，不要直接重试。`,
          { title: '部分开卡失败' },
        )
        loadCards()
        loadOrders()
        return
      }
      return dialog.toast(errText(r, '开卡失败'), 'err')
    }
    applyOpen.value = false
    await dialog.alert(`已创建 ${orderNos.length} 个开卡单：\n${orderNos.join('\n')}`, { title: '开卡已提交' })
    loadCards()
    loadOrders()
  } finally {
    applying.value = false
  }
}

// 导入卡
const importOpen = ref(false)
const importing = ref(false)
const importForm = reactive({ card_id: '', product_id: '', card_holder_id: '' })

async function openImportCard() {
  if (!holders.value.length) await loadHolders()
  importOpen.value = true
}
async function submitImportCard() {
  if (!importForm.card_id.trim() || !importForm.product_id) return dialog.toast('请填写 card_id 并选择产品', 'warn')
  importing.value = true
  try {
    const r = await api('/cards/import', 'POST', {
      card_id: importForm.card_id.trim(), product_id: importForm.product_id, card_holder_id: importForm.card_holder_id,
    })
    if (!r.ok) return dialog.toast(errText(r, '导入失败'), 'err')
    dialog.toast('已导入', 'ok')
    importOpen.value = false
    importForm.card_id = ''
    loadCards()
  } finally {
    importing.value = false
  }
}

// 详情
const detail = ref<{ card: Card; orders: Order[]; transactions: Txn[] } | null>(null)
const detailOpen = ref(false)
async function openDetail(row: Card) {
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}`)
  if (!r.ok) return dialog.toast(errText(r, '加载详情失败'), 'err')
  detail.value = r.data
  detailOpen.value = true
}

// ---------- 持卡人 ----------
const holders = ref<Holder[]>([])
const holdersLoading = ref(false)
const importHolderId = ref('')
const holderImporting = ref(false)

async function loadHolders() {
  holdersLoading.value = true
  try {
    const r = await api<{ items: Holder[] }>('/cardholders')
    if (!r.ok) return dialog.toast(errText(r, '加载持卡人失败'), 'err')
    holders.value = r.data.items
  } finally {
    holdersLoading.value = false
  }
}
async function importHolder() {
  const id = importHolderId.value.trim()
  if (!id) return dialog.toast('请输入持卡人 ID', 'warn')
  holderImporting.value = true
  try {
    const r = await api('/cardholders/import', 'POST', { card_holder_id: id })
    if (!r.ok) return dialog.toast(errText(r, '导入失败'), 'err')
    dialog.toast('已导入', 'ok')
    importHolderId.value = ''
    loadHolders()
  } finally {
    holderImporting.value = false
  }
}
async function refreshHolder(row: Holder) {
  const r = await api(`/cardholders/${encodeURIComponent(row.card_holder_id)}/refresh`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '刷新失败'), 'err')
  dialog.toast('已刷新', 'ok')
  loadHolders()
}

// ---------- 单据 ----------
const orders = ref<Order[]>([])
const orderTotal = ref(0)
const ordersLoading = ref(false)
const orderFilter = reactive({ state: '', kind: '', page: 1 })

async function loadOrders() {
  ordersLoading.value = true
  try {
    const q = new URLSearchParams({ page: String(orderFilter.page), page_size: String(PAGE_SIZE) })
    if (orderFilter.state) q.set('state', orderFilter.state)
    if (orderFilter.kind) q.set('kind', orderFilter.kind)
    const r = await api<{ items: Order[]; total: number }>('/orders?' + q)
    if (!r.ok) return dialog.toast(errText(r, '加载单据失败'), 'err')
    orders.value = r.data.items
    orderTotal.value = r.data.total
  } finally {
    ordersLoading.value = false
  }
}
function resetOrders() {
  orderFilter.page = 1
  loadOrders()
}
async function recheckOrder(row: Order) {
  const r = await api(`/orders/${encodeURIComponent(row.out_order_no)}/recheck`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '查询失败'), 'err')
  dialog.toast('已重新查询', 'ok')
  loadOrders()
}
async function confirmFailed(row: Order) {
  const note = await dialog.prompt(
    `确认单据 ${row.out_order_no}（${kindLabel(row.kind)} ${money(row.amount_cents)}）已失败？\n请先在平台核实确未执行。请填写备注（必填）：`,
    { title: '确认失败', placeholder: '备注', okText: '确认失败' },
  )
  if (note === null || note === undefined) return
  const text = String(note).trim()
  if (!text) return dialog.toast('请填写备注', 'warn')
  const r = await api(`/orders/${encodeURIComponent(row.out_order_no)}/confirm-failed`, 'POST', { note: text })
  if (!r.ok) return dialog.toast(errText(r, '操作失败'), 'err')
  dialog.toast('已确认失败', 'ok')
  loadOrders()
}

// ---------- 交易 ----------
const txns = ref<Txn[]>([])
const txnTotal = ref(0)
const txnsLoading = ref(false)
const txnFilter = reactive({ card_id: '', page: 1 })

async function loadTxns() {
  txnsLoading.value = true
  try {
    const q = new URLSearchParams({ page: String(txnFilter.page), page_size: String(PAGE_SIZE) })
    if (txnFilter.card_id.trim()) q.set('card_id', txnFilter.card_id.trim())
    const r = await api<{ items: Txn[]; total: number }>('/transactions?' + q)
    if (!r.ok) return dialog.toast(errText(r, '加载交易失败'), 'err')
    txns.value = r.data.items
    txnTotal.value = r.data.total
  } finally {
    txnsLoading.value = false
  }
}
function resetTxns() {
  txnFilter.page = 1
  loadTxns()
}

// ---------- 策略与健康度 ----------
const policy = ref<Policy | null>(null)
const openAmountText = ref('0.00')
const policySaving = ref(false)
const policyError = ref('')
const newPlanKey = ref('')
const health = ref<Health[]>([])
const healthLoading = ref(false)

const planKeys = computed(() => {
  const keys = Object.keys(policy.value?.plan_products || {})
  return keys.sort((a, b) => (a === 'default' ? -1 : b === 'default' ? 1 : a.localeCompare(b)))
})

// 目标数表：覆盖全部已知产品，保存时只保留有值的
const targetRows = computed(() => {
  const p = policy.value
  if (!p) return []
  const ids = new Set<string>([...products.value.map((x) => x.product_id), ...Object.keys(p.product_targets)])
  return [...ids].map((id) => {
    if (!p.product_targets[id]) p.product_targets[id] = { target: 0, daily_limit: 0 }
    return { id, target: p.product_targets[id] }
  })
})

async function loadPolicy() {
  policyError.value = ''
  const r = await api<Policy>('/policy')
  if (!r.ok) return dialog.toast(errText(r, '加载策略失败'), 'err')
  const p = r.data
  p.plan_products = p.plan_products || {}
  p.product_targets = p.product_targets || {}
  p.holder_ids = p.holder_ids || []
  policy.value = p
  openAmountText.value = money(p.open_amount_cents)
}

function addProduct(key: string, pid: string) {
  if (pid && policy.value && !policy.value.plan_products[key].includes(pid)) policy.value.plan_products[key].push(pid)
}
function moveProduct(key: string, i: number, d: number) {
  const list = policy.value!.plan_products[key]
  const j = i + d
  if (j < 0 || j >= list.length) return
  ;[list[i], list[j]] = [list[j], list[i]]
}
function addPlan() {
  const k = newPlanKey.value.trim()
  if (!k || !policy.value) return
  if (!policy.value.plan_products[k]) policy.value.plan_products[k] = []
  newPlanKey.value = ''
}
function removePlan(key: string) {
  if (policy.value) delete policy.value.plan_products[key]
}

async function savePolicy() {
  if (!policy.value) return
  policyError.value = ''
  const amt = openAmountText.value.trim()
  if (!/^\d+(\.\d{1,2})?$/.test(amt)) {
    policyError.value = '开卡金额无效，须为不少于 0 且最多两位小数的数字'
    return
  }
  // PUT 会替换整份策略：带上从服务器加载的全部字段，只改表单里编辑的值。
  const body: Policy = {
    ...policy.value,
    open_amount_cents: Math.round(Number(amt) * 100),
    product_targets: Object.fromEntries(
      Object.entries(policy.value.product_targets).filter(([, t]) => t.target > 0 || t.daily_limit > 0),
    ),
  }
  policySaving.value = true
  try {
    const r = await api<Policy>('/policy', 'PUT', body)
    if (!r.ok) {
      policyError.value = errText(r, '保存失败')
      return
    }
    dialog.toast('策略已保存', 'ok')
    await loadPolicy()
  } finally {
    policySaving.value = false
  }
}

async function loadHealth() {
  healthLoading.value = true
  try {
    const r = await api<{ items: Health[] }>('/health')
    if (!r.ok) return dialog.toast(errText(r, '加载健康度失败'), 'err')
    health.value = r.data.items
  } finally {
    healthLoading.value = false
  }
}
async function resumeProduct(row: Health) {
  const ok = await dialog.confirm(`恢复产品 ${productLabel(row.product_id)}（套餐 ${row.plan_key || '—'}）？`, { title: '恢复', okText: '恢复' })
  if (!ok) return
  const r = await api('/health/resume', 'POST', { product_id: row.product_id, plan_key: row.plan_key })
  if (!r.ok) return dialog.toast(errText(r, '恢复失败'), 'err')
  dialog.toast('已恢复', 'ok')
  loadHealth()
}

// ---------- 标签切换 / 生命周期 ----------
watch(tab, (t) => {
  if (t === 'overview') loadOverview()
  else if (t === 'cards') loadCards()
  else if (t === 'holders') loadHolders()
  else if (t === 'orders') loadOrders()
  else if (t === 'txns') loadTxns()
  else if (t === 'policy') {
    if (!holders.value.length) loadHolders()
    loadPolicy()
    loadHealth()
  }
})

onMounted(() => {
  loadProducts()
  loadOverview()
})

onBeforeUnmount(() => {
  clearSensitive()
  clearOtp()
})
</script>

<style scoped>
.text-warn {
  color: #d97706;
}
.field-error {
  margin-bottom: 8px;
  font-size: 13px;
  color: #dc2626;
}
.pending-chip {
  padding: 8px 14px;
  border-radius: 8px;
  border: 1px solid rgba(127, 127, 127, 0.3);
  background: rgba(127, 127, 127, 0.06);
  cursor: pointer;
  font-size: 14px;
}
.pending-chip b {
  margin-left: 6px;
  font-size: 16px;
}
.pending-chip:hover {
  border-color: #3b82f6;
}
.grid-form {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  column-gap: 16px;
}
.plan-box {
  padding: 10px 12px;
  margin-bottom: 8px;
  border: 1px solid rgba(127, 127, 127, 0.25);
  border-radius: 8px;
}
.otp {
  font-size: 22px;
  letter-spacing: 3px;
}
</style>
