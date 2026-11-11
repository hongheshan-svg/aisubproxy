<template>
  <div class="space-y-4">
    <AdminPageHeader title="MarsWallex 卡池" description="库存、卡、持卡人、单据、交易，以及开卡与补卡策略" class="!mb-0" />

    <div class="card">
      <el-tabs v-model="tab">
        <!-- ============ 概览 ============ -->
        <el-tab-pane label="概览" name="overview">
          <div class="flex justify-end mb-2">
            <el-button size="small" :loading="ovLoading" @click="loadOverview">刷新</el-button>
          </div>
          <ApiErrorNotice v-if="ovConfigError" :message="ovConfigError" />
          <div v-if="overview" class="space-y-4">
            <div>
              <div class="text-xs uppercase tracking-wide text-muted mb-1">主钱包余额</div>
              <ApiErrorNotice v-if="overview.balance_error" :message="overview.balance_error" />
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
                <div v-if="usedStateNote(row)" class="text-xs text-subtle mt-1">{{ usedStateNote(row) }}</div>
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
                  <el-button v-if="row.pool_state !== 'retired'" size="small" plain @click="openLimit(row)">限额</el-button>
                  <template v-if="row.pool_state === 'available'">
                    <el-button size="small" type="primary" plain @click="leaseCard(row)">租用</el-button>
                    <el-button size="small" type="warning" plain @click="disableCard(row)">停用</el-button>
                  </template>
                  <template v-if="isMine(row)">
                    <el-button size="small" type="primary" plain @click="fundCard(row)">注资</el-button>
                    <el-button size="small" plain @click="releaseCard(row)">释放</el-button>
                  </template>
                  <el-button v-if="canViewSensitive(row)" size="small" type="primary" plain @click="viewSensitive(row)">查看卡号</el-button>
                  <el-button v-if="row.pool_state !== 'retired'" size="small" type="primary" plain @click="viewOtp(row)">查看验证码</el-button>
                  <el-button v-if="row.pool_state === 'disabled'" size="small" type="success" plain @click="enableCard(row)">启用</el-button>
                  <el-button v-if="canFreeze(row.platform_status)" size="small" plain @click="runOrderAction(row, 'freeze', '冻结')">冻结</el-button>
                  <el-button v-if="canUnfreeze(row.platform_status)" size="small" plain @click="runOrderAction(row, 'unfreeze', '解冻')">解冻</el-button>
                  <template v-if="row.pool_state !== 'retired'">
                    <el-button size="small" plain @click="runCardAction(row, 'refresh', '已刷新')">刷新</el-button>
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
            <el-button type="primary" size="small" @click="openCreateHolder">新建持卡人</el-button>
          </div>

          <div class="mb-4">
            <div class="flex items-center gap-2 mb-1">
              <span class="text-sm font-semibold text-ink">提交记录</span>
              <el-button size="small" text :loading="subsLoading" @click="loadSubmissions">刷新</el-button>
            </div>
            <el-table :data="submissions" size="small" border empty-text="暂无提交记录" v-loading="subsLoading">
              <el-table-column prop="out_card_holder_id" label="提交编号" min-width="190" show-overflow-tooltip />
              <el-table-column label="姓名" min-width="130">
                <template #default="{ row }">{{ row.first_name }} {{ row.last_name }}</template>
              </el-table-column>
              <el-table-column prop="country_code" label="国家" width="70" />
              <el-table-column label="状态" width="110">
                <template #default="{ row }">
                  <el-tag size="small" :type="submissionTagType(row)">{{ submissionLabel(row) }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="card_holder_id" label="持卡人 ID" min-width="170" show-overflow-tooltip />
              <el-table-column prop="last_error" label="错误" min-width="140" show-overflow-tooltip />
              <el-table-column label="提交时间" min-width="150">
                <template #default="{ row }">{{ fmtTs(row.created_at) }}</template>
              </el-table-column>
              <el-table-column label="操作" width="80">
                <template #default="{ row }">
                  <el-button v-if="canAbandonSubmission(row)" size="small" type="danger" plain @click="abandonSubmission(row)">放弃</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
          <el-table :data="holders" size="small" border empty-text="暂无持卡人" v-loading="holdersLoading">
            <el-table-column prop="card_holder_id" label="持卡人 ID" min-width="190" show-overflow-tooltip />
            <el-table-column label="姓名" min-width="140">
              <template #default="{ row }">{{ row.first_name }} {{ row.last_name }}</template>
            </el-table-column>
            <el-table-column prop="country_code" label="国家" width="80" />
            <el-table-column prop="status" label="状态" width="100" />
            <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
            <el-table-column label="最近同步/查询" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.synced_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="230">
              <template #default="{ row }">
                <el-button size="small" plain @click="refreshHolder(row)">刷新</el-button>
                <el-button size="small" plain @click="openEditHolder(row)">修改</el-button>
                <el-button v-if="row.status === 'APPROVED'" size="small" type="danger" plain @click="setHolderStatus(row, 'DISABLED')">停用</el-button>
                <el-button v-else-if="row.status === 'DISABLED'" size="small" plain @click="setHolderStatus(row, 'APPROVED')">启用</el-button>
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
            <el-dropdown trigger="click" :disabled="backfilling" @command="runBackfill">
              <el-button size="small" type="primary" :loading="backfilling">立即补拉</el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item :command="1">补拉 1 天</el-dropdown-item>
                  <el-dropdown-item :command="3">补拉 3 天</el-dropdown-item>
                  <el-dropdown-item :command="7">补拉 7 天</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
          <div v-if="backfill" class="text-sm text-muted mb-3 space-y-1">
            <div>
              已补拉到：{{ fmtTs(backfill.cursor) }}　上次运行：{{ fmtTs(backfill.last_run_at) }}　新增 {{ backfill.last_added ?? 0 }} 条
            </div>
            <div v-if="backfill.last_error" class="text-warn">上次错误：{{ backfill.last_error }}</div>
            <div v-if="backfilling">补拉进行中，可能需要一段时间，请勿重复点击……</div>
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

        <!-- ============ 开卡与补卡策略 ============ -->
        <el-tab-pane label="开卡与补卡策略" name="policy">
          <div class="text-sm text-muted mb-3" data-test="selection-moved">
            选卡规则（各套餐的卡产品优先级、自动停用、健康度）已移到
            <router-link to="/ops/marswallex-selection" class="text-primary">MarsWallex → 选卡规则</router-link>。
          </div>
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
                <el-form-item label="租期（分钟，5-240）">
                  <el-input-number v-model="policy.lease_minutes" :min="5" :max="240" />
                </el-form-item>
                <el-form-item label="用完后注销等待（天，0-90）">
                  <el-input-number v-model="policy.cancel_after_days" :min="0" :max="90" />
                </el-form-item>
              </div>

              <div class="text-sm font-semibold text-ink mb-1">资金上限（美元）</div>
              <p class="text-xs text-muted mb-2">超过上限的注资或转账会被后端直接拒绝，不能填 0 或负数，也没有「不限」。</p>
              <div class="grid-form">
                <el-form-item label="单笔注资上限">
                  <el-input v-model="limitText.fund" placeholder="例如 500.00" inputmode="decimal" data-field="max_fund" />
                </el-form-item>
                <el-form-item label="钱包操作上限（单笔转账、兑换）">
                  <el-input v-model="limitText.wallet" placeholder="例如 2000.00" inputmode="decimal" data-field="max_wallet" />
                </el-form-item>
                <el-form-item label="单日注资累计上限（按北京日）">
                  <el-input v-model="limitText.daily" placeholder="例如 2000.00" inputmode="decimal" data-field="daily_cap" />
                </el-form-item>
                <el-form-item label="单日注资笔数上限（按北京日，笔）">
                  <el-input-number v-model="policy.daily_fund_count_cap" :min="1" :max="100000" :step="10" :precision="0" data-field="daily_count_cap" />
                  <div class="text-xs text-muted mt-1">充值和带金额的开卡都算一笔，明确失败、没发出的不算。到上限后当天不再注资，发码和兑换也会暂停，北京时间次日 0 点恢复。</div>
                </el-form-item>
                <el-form-item label="未选用 MarsWallex 时仍自动补卡">
                  <el-switch v-model="policy.replenish_when_unselected" />
                  <div class="text-xs text-muted mt-1">默认发码服务商不是 MarsWallex 时，自动补卡会暂停并每日告警一次。打开后仍会补卡，会占用主钱包余额。</div>
                </el-form-item>
              </div>

              <el-form-item label="开卡持卡人（可多选）">
                <el-select v-model="policy.holder_ids" multiple filterable allow-create default-first-option style="width: 100%" placeholder="选择或输入持卡人 ID">
                  <el-option v-for="h in holders" :key="h.card_holder_id" :value="h.card_holder_id"
                    :label="`${h.card_holder_id}（${h.first_name} ${h.last_name}）`" />
                </el-select>
              </el-form-item>

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
          <div class="flex w-full gap-2">
            <el-input v-model="importForm.card_id" class="mono" @input="resetPreview" @keyup.enter="queryImportCard" />
            <el-button :loading="previewing" :disabled="!importForm.card_id.trim()" @click="queryImportCard">查询</el-button>
          </div>
          <p class="text-xs text-subtle mt-1">在 MarsWallex 平台的卡详情里找，是一串数字，不是卡号。平台没有卡列表接口，只能手动填。</p>
        </el-form-item>
        <div v-if="preview" class="mb-3 space-y-1 text-sm">
          <div class="text-muted">
            {{ preview.card_label }} · {{ preview.card_country }} · {{ preview.card_currency }} · {{ preview.card_mode }} · 状态 {{ preview.status }}
          </div>
          <div class="text-muted">
            余额 {{ money(preview.available_cents) }}（冻结 {{ money(preview.frozen_cents) }}）· 有效期 {{ preview.expiry || '—' }} ·
            <span class="mono">{{ preview.first6 || '······' }}…{{ preview.last4 || '····' }}</span>
          </div>
          <div v-if="preview.already_in_pool" class="text-warn">已在池中</div>
          <div v-if="importSweepWarning(preview.available_cents)" class="text-warn">{{ importSweepWarning(preview.available_cents) }}</div>
        </div>
        <el-form-item label="产品">
          <el-select v-model="importForm.product_id" filterable style="width: 100%">
            <el-option v-for="p in importProducts" :key="p.product_id" :label="productLabel(p.product_id) + (p.matched ? '（匹配）' : '')" :value="p.product_id" />
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
        <el-button type="primary" :loading="importing" :disabled="!preview || preview.already_in_pool" @click="submitImportCard">导入</el-button>
      </template>
    </el-dialog>

    <!-- 单卡限额 -->
    <el-dialog v-model="limitOpen" :title="`卡限额 ${limitCard?.card_id || ''}`" width="480px" :close-on-click-modal="false" append-to-body>
      <div class="space-y-3" v-loading="limitLoading">
        <div v-if="limitLoadError" class="alert alert-error text-sm">
          读取当前限额失败，不能在不知道当前值的情况下修改：{{ limitLoadError }}
          <el-button size="small" link type="primary" @click="loadLimit">重试</el-button>
        </div>
        <template v-else>
          <div class="alert alert-warning text-sm">
            设置限额可能导致付款被拒：付款金额超过单笔、当日或当月限额时，平台会直接拒绝。
            这是整体替换，没填的项会变成不限制；至少填一项。
          </div>
          <el-form label-position="top" @submit.prevent>
            <el-form-item label="单笔限额（美元，留空不限）"><el-input v-model="limitForm.single" inputmode="decimal" /></el-form-item>
            <el-form-item label="单日限额（美元，留空不限）"><el-input v-model="limitForm.daily" inputmode="decimal" /></el-form-item>
            <el-form-item label="单月限额（美元，留空不限）"><el-input v-model="limitForm.monthly" inputmode="decimal" /></el-form-item>
          </el-form>
          <div v-if="limitRemaining" class="text-sm text-muted">
            剩余：单日 {{ limitRemaining.daily_remaining ?? '未设限' }}；单月 {{ limitRemaining.monthly_remaining ?? '未设限' }}
          </div>
          <p v-if="limitError" class="field-error">{{ limitError }}</p>
        </template>
      </div>
      <template #footer>
        <el-button @click="limitOpen = false">取消</el-button>
        <el-button type="primary" :loading="limitSaving" :disabled="!!limitLoadError || limitLoading" @click="saveLimit">保存限额</el-button>
      </template>
    </el-dialog>

    <!-- 卡详情 -->
    <el-drawer v-model="detailOpen" :title="`卡详情 ${detail?.card.card_id || ''}`" size="720px" @closed="detail = null; balTxns = []">
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
          <div class="text-sm font-semibold text-ink mb-1">平台转入转出明细</div>
          <p v-if="balTxnsError" class="text-sm text-warn mb-1">{{ balTxnsError }}</p>
          <p v-if="balTxns.some((x) => x.mismatch)" class="field-error">与本地单据不一致，请到『单据』中重查</p>
          <el-table :data="balTxns" size="small" border empty-text="暂无明细" v-loading="balTxnsLoading"
            :row-class-name="({ row }: { row: BalTxn }) => (row.mismatch ? 'mismatch-row' : '')">
            <el-table-column label="来源" width="80">
              <template #default="{ row }">{{ row.origin === 'external' ? '外部' : '本系统' }}</template>
            </el-table-column>
            <el-table-column label="类型" width="80">
              <template #default="{ row }">{{ row.type === 'CHARGE' ? '转入' : row.type === 'WITHDRAW' ? '转出' : row.type }}</template>
            </el-table-column>
            <el-table-column label="金额" min-width="110">
              <template #default="{ row }">{{ row.amount }} {{ row.currency }}</template>
            </el-table-column>
            <el-table-column prop="status" label="状态" width="90" />
            <el-table-column label="时间" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.create_time) }}</template>
            </el-table-column>
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

    <!-- 持卡人资料表单：关闭即销毁并清空，不落任何浏览器存储 -->
    <el-dialog v-model="holderFormOpen" :title="holderFormMode === 'edit' ? '修改持卡人' : '新建持卡人'" width="760px"
      destroy-on-close :close-on-click-modal="false" @closed="holderInitial = undefined">
      <CardholderForm v-if="holderFormOpen" :mode="holderFormMode" :initial="holderInitial" :submitting="holderSubmitting"
        @submitted="submitHolder" @cancel="holderFormOpen = false" />
    </el-dialog>

    <!-- 卡号弹窗：关闭即清空 -->
    <el-dialog v-model="sensitiveOpen" title="卡号（已打码）" width="440px" destroy-on-close :close-on-click-modal="false" @closed="clearSensitive">
      <el-descriptions v-if="sensitive" :column="1" border size="small">
        <el-descriptions-item label="卡号"><span class="mono">{{ sensitive.cardNoMasked }}</span></el-descriptions-item>
      </el-descriptions>
      <p class="text-xs text-subtle mt-2">只显示前 6 位和后 4 位；完整卡号和 CVV2 不在后台显示。</p>
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
import { fmtBeijingAny } from '../../lib/mwwebhook'
import ApiErrorNotice from '../../components/ApiErrorNotice.vue'
import { mapApiError } from '../../lib/errors'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouteTab } from '../../lib/useRouteTab'
import { centsToUsd, usdToCents } from '../../lib/provider'
import { useRouter } from 'vue-router'
import CardholderForm from './components/CardholderForm.vue'
import {
  ORDER_KINDS, canAbandonSubmission, errText, submissionLabel, submissionTagType, fmtTs, isUncertain, kindLabel, money, poolApi as api,
  orderStateText, orderToast, withoutSelection, type CardholderFormValue,
} from '../../lib/cardpool'
import {
  canFreeze, canUnfreeze, importProductOptions, importSweepWarning, previewMatchesInput, releaseConfirmText, releaseDoneText, sensitiveNote, usedStateNote,
} from '../../lib/mwcardtext'
import { toSensitiveView, type SensitiveView } from '../../lib/cardpool-sensitive'
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
  lease_sensitive?: boolean; cancel_after?: number | null
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
  max_fund_cents: number
  max_wallet_op_cents: number
  daily_fund_cap_cents: number
  daily_fund_count_cap: number
  replenish_when_unselected: boolean
}

const PAGE_SIZE = 20
const POOL_STATES = [
  { value: 'available', label: '可用' }, { value: 'leased', label: '租用中' },
  { value: 'releasing', label: '清零中' }, { value: 'used', label: '已用' },
  { value: 'retired', label: '已注销' }, { value: 'disabled', label: '停用' },
]
const ORDER_STATES = ['pending', 'processing', 'unknown', 'needs_review', 'success', 'failed', 'not_sent']

const router = useRouter()
const auth = useAuthStore()
const tab = useRouteTab('/ops/cardpool')

// ---------- 通用 ----------
function fmtIso(s: string) {
  return fmtBeijingAny(s)
}

function poolStateLabel(s: string) {
  return POOL_STATES.find((x) => x.value === s)?.label || s
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

// 查看卡号：本人租用中的卡（计为本次租约已取卡号）；已用、停用、清零中的卡仅供排查，只查看、不改状态。
// 可用的卡要先租用；别人租用中的卡不提供（会算到对方的租约上）。
function canViewSensitive(c: Card) {
  return isMine(c) || ['used', 'disabled', 'releasing'].includes(c.pool_state)
}

// ---------- 概览 ----------
const overview = ref<Overview | null>(null)
const ovLoading = ref(false)
const ovConfigError = ref('')

async function loadOverview() {
  ovLoading.value = true
  try {
    const r = await api<Overview>('/overview')
    if (!r.ok) {
      // 未配置凭证：只在页面内给一个带「去配置」的提示，不再弹 toast
      const m = mapApiError(errText(r, '加载概览失败'))
      if (m.config) {
        ovConfigError.value = m.raw
        overview.value = null
        return
      }
      return dialog.toast(m.raw, 'err')
    }
    ovConfigError.value = ''
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
  if (r.ok) {
    products.value = r.data.items || []
    ensureTargetRows()
  } else if (!mapApiError(errText(r, '')).config) {
    // 未配置凭证已在概览里提示（带「去配置」），这里不再重复弹 toast
    dialog.toast(errText(r, '加载产品失败'), 'warn')
  }
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

// 返回单据的动作（冻结、解冻、注销、清零、注资）：按返回单据的状态提示
async function runOrderAction(row: Card, action: string, what: string, body?: unknown) {
  const r = await api<Order>(`/cards/${encodeURIComponent(row.card_id)}/${action}`, 'POST', body)
  if (!r.ok) {
    dialog.toast(errText(r, '操作失败'), 'err')
    return false
  }
  orderToast(what, r.data)
  loadCards()
  return true
}

async function leaseCard(row: Card) {
  await runCardAction(row, 'lease', '已租用')
}
async function releaseCard(row: Card) {
  const ok = await dialog.confirm(releaseConfirmText(row, policy.value), { title: '释放', okText: '释放' })
  if (!ok) return
  // 去向按点击时的使用次数与策略预判，成功后提示
  await runCardAction(row, 'release', releaseDoneText(row, policy.value))
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
  const ok = await dialog.confirm(`确认把卡 ${row.card_id}（后四位 ${row.last4}）上的可用余额全部转回主钱包？`, {
    title: '清零', okText: '清零',
  })
  if (!ok) return
  await runOrderAction(row, 'sweep', '清零')
}
async function cancelCard(row: Card) {
  const ok = await dialog.confirm(`确认注销卡 ${row.card_id}（后四位 ${row.last4}）？注销后不可恢复。`, {
    title: '注销', okText: '注销', danger: true,
  })
  if (!ok) return
  await runOrderAction(row, 'cancel', '注销')
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
  await runOrderAction(row, 'fund', '注资', { amount })
}

// 敏感信息：只存在内存里，关闭弹窗即清空；不写日志、不进存储。
const sensitive = ref<SensitiveView | null>(null)
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
  const note = sensitiveNote(row.pool_state, policy.value)
  const ok = await dialog.confirm(`将显示卡 ${row.card_id}（后四位 ${row.last4}）的打码卡号（前 6 后 4），此操作会被记录。${note}继续？`, {
    title: '查看卡号', okText: '查看',
  })
  if (!ok) return
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}/sensitive`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '获取卡号失败'), 'err')
  sensitive.value = toSensitiveView(r.data)
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
    const created = r.data.orders || []
    const lines = created.map((o) => `${o.out_order_no}　${orderStateText(o)}`).join('\n')
    if (!r.ok) {
      if (created.length) {
        // 部分已创建：必须让管理员看到，避免盲目重试多开卡。
        applyOpen.value = false
        await dialog.alert(
          `开卡中途失败：${errText(r, '未知错误')}\n\n已创建 ${r.data.created ?? created.length} 张：\n${lines}\n\n请先到「单据」标签核对，不要直接重试。`,
          { title: '部分开卡失败' },
        )
        loadCards()
        loadOrders()
        return
      }
      return dialog.toast(errText(r, '开卡失败'), 'err')
    }
    applyOpen.value = false
    // 中途有失败、未发出或结果未知的单据时后端会停止本批，不足申请数量
    const allOk = created.every((o) => ['success', 'processing', 'pending'].includes(o.state))
    const stopped = created.length < applyForm.count
      ? `\n\n申请 ${applyForm.count} 张：最后一张单据失败、未发出或结果未知，本批已停止。结果未知的单据可能已开出卡，请先到「单据」标签核对，不要直接重试。`
      : ''
    await dialog.alert(`已创建 ${created.length} 个开卡单：\n${lines}${stopped}`, { title: allOk ? '开卡已提交' : '开卡结果' })
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

interface CardPreview {
  card_id: string; card_label: string; card_country: string; card_currency: string; card_mode: string; status: string
  available_cents: number; frozen_cents: number; expiry: string; first6: string; last4: string
  already_in_pool: boolean; suggested_product_id: string; candidates: string[]
}
const preview = ref<CardPreview | null>(null)
const previewing = ref(false)
// 列出全部产品，查询得到的候选排前面并标「（匹配）」
const importProducts = computed(() => importProductOptions(products.value, preview.value?.candidates))
function resetPreview() {
  preview.value = null
  importForm.product_id = ''
}
async function queryImportCard() {
  const id = importForm.card_id.trim()
  if (!id) return
  previewing.value = true
  try {
    const r = await api<CardPreview>('/cards/preview', 'POST', { card_id: id })
    // 查询在途时改了卡 ID：旧结果作废，不写入 preview 和产品
    if (importForm.card_id.trim() !== id) return
    if (!r.ok) {
      preview.value = null
      return dialog.toast(errText(r, '查询失败'), 'err')
    }
    preview.value = r.data
    importForm.product_id = r.data.suggested_product_id || ''
  } finally {
    previewing.value = false
  }
}

async function openImportCard() {
  if (!holders.value.length) await loadHolders()
  resetPreview()
  importOpen.value = true
}
async function submitImportCard() {
  if (!importForm.card_id.trim() || !importForm.product_id) return dialog.toast('请填写 card_id 并选择产品', 'warn')
  const pv = preview.value // 确认框期间输入可能被改，先取下来
  if (!pv || !previewMatchesInput(pv.card_id, importForm.card_id)) {
    return dialog.toast('卡 ID 已改动，请重新查询', 'warn')
  }
  if (pv.already_in_pool) return
  const warn = importSweepWarning(pv.available_cents)
  if (warn && !(await dialog.confirm(warn, { title: '导入卡', okText: '导入' }))) return
  importing.value = true
  try {
    const r = await api('/cards/import', 'POST', {
      card_id: pv.card_id, product_id: importForm.product_id, card_holder_id: importForm.card_holder_id,
    })
    if (!r.ok) return dialog.toast(errText(r, '导入失败'), 'err')
    dialog.toast('已导入', 'ok')
    importOpen.value = false
    importForm.card_id = ''
    preview.value = null
    loadCards()
  } finally {
    importing.value = false
  }
}

// 详情
const detail = ref<{ card: Card; orders: Order[]; transactions: Txn[] } | null>(null)
const detailOpen = ref(false)
interface BalTxn {
  out_order_no: string; transaction_id: string; card_id: string; currency: string; amount: string
  type: string; status: string; create_time: number; origin: string; local_state: string; mismatch: boolean
}
const balTxns = ref<BalTxn[]>([])
const balTxnsLoading = ref(false)
const balTxnsError = ref('')
async function openDetail(row: Card) {
  const r = await api(`/cards/${encodeURIComponent(row.card_id)}`)
  if (!r.ok) return dialog.toast(errText(r, '加载详情失败'), 'err')
  detail.value = r.data
  detailOpen.value = true
  loadBalTxns(row.card_id)
}
async function loadBalTxns(cardId: string) {
  balTxns.value = []
  balTxnsError.value = ''
  balTxnsLoading.value = true
  try {
    const r = await api<{ items: BalTxn[] }>(`/cards/${encodeURIComponent(cardId)}/balance-transactions`)
    if (detail.value?.card.card_id !== cardId) return // 抽屉已关或换了卡
    if (!r.ok) {
      balTxnsError.value = errText(r, '加载平台转入转出明细失败')
      return
    }
    balTxns.value = r.data.items || []
  } finally {
    balTxnsLoading.value = false
  }
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

// 持卡人新建 / 修改
interface Submission {
  out_card_holder_id: string; first_name: string; last_name: string; country_code: string
  state: string; card_holder_id: string; last_error: string; requested_by: string; created_at: number; updated_at: number
}
const submissions = ref<Submission[]>([])
const subsLoading = ref(false)
async function loadSubmissions() {
  subsLoading.value = true
  try {
    const r = await api<{ items: Submission[] }>('/cardholder-submissions')
    if (!r.ok) return dialog.toast(errText(r, '加载提交记录失败'), 'err')
    submissions.value = r.data.items || []
  } finally {
    subsLoading.value = false
  }
}
async function abandonSubmission(row: Submission) {
  const ok = await dialog.confirm(
    `放弃提交 ${row.out_card_holder_id}？\n放弃后该记录不再等待审核推送自动关联；如平台实际已创建，请用「按 ID 导入」。`,
    { title: '放弃提交', okText: '放弃' },
  )
  if (!ok) return
  const r = await api(`/cardholder-submissions/${encodeURIComponent(row.out_card_holder_id)}/abandon`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '操作失败'), 'err')
  dialog.toast('已放弃', 'ok')
  loadSubmissions()
}

const holderFormOpen = ref(false)
const holderFormMode = ref<'create' | 'edit'>('create')
const holderInitial = ref<CardholderFormValue | undefined>(undefined)
const holderSubmitting = ref(false)
const editingHolderId = ref('')

function openCreateHolder() {
  holderFormMode.value = 'create'
  holderInitial.value = undefined
  editingHolderId.value = ''
  holderFormOpen.value = true
}
async function openEditHolder(row: Holder) {
  // 完整资料只放在表单内存里，对话框关闭即清空；不缓存、不打日志。
  const r = await api(`/cardholders/${encodeURIComponent(row.card_holder_id)}/profile`)
  if (!r.ok) return dialog.toast(errText(r, '加载持卡人资料失败'), 'err')
  const d = r.data || {}
  const addr = (a: any) => ({
    country: a?.country || '', state: a?.state || '', city: a?.city || '', address: a?.address || '', post_code: a?.post_code || '',
  })
  const res = addr(d.residential_address)
  const bil = addr(d.billing_address)
  holderInitial.value = {
    first_name: d.first_name || '', last_name: d.last_name || '',
    chinese_first_name: d.chinese_first_name || '', chinese_last_name: d.chinese_last_name || '',
    date_of_birth: d.date_of_birth || '', zone_number: d.zone_number || '', mobile: d.mobile || '', email: d.email || '',
    country_code: d.country_code || '', id_type: d.id_type || 'PASSPORT', id_no: d.id_no || '',
    id_period_begin: d.id_period_begin || '', id_period_end: d.id_period_end || '',
    id_photo_file_ids: Array.isArray(d.id_photo_file_ids) ? [...d.id_photo_file_ids] : [],
    residential_address: res, billing_address: bil,
    billing_same: JSON.stringify(res) === JSON.stringify(bil), ssn: d.ssn || '',
  }
  holderFormMode.value = 'edit'
  editingHolderId.value = row.card_holder_id
  holderFormOpen.value = true
}
async function submitHolder(payload: Record<string, unknown>) {
  if (holderSubmitting.value) return
  holderSubmitting.value = true
  try {
    if (holderFormMode.value === 'edit') {
      const r = await api(`/cardholders/${encodeURIComponent(editingHolderId.value)}`, 'PUT', payload)
      if (isUncertain(r)) {
        dialog.toast('结果未知：请稍后刷新持卡人列表确认，不要立即重复提交', 'warn')
        return
      }
      if (!r.ok) return dialog.toast(errText(r, '修改失败'), 'err')
      holderFormOpen.value = false
      dialog.toast('已提交修改', 'ok')
      loadHolders()
      return
    }
    const r = await api('/cardholders', 'POST', payload)
    if (r.status === 202 || isUncertain(r)) {
      holderFormOpen.value = false
      dialog.toast('结果未知；审核推送到达或手动导入后会自动关联', 'warn')
      loadSubmissions()
      return
    }
    if (!r.ok) return dialog.toast(errText(r, '新建失败'), 'err')
    holderFormOpen.value = false
    dialog.toast('已提交，审核通过后需在『策略』中手动加入补卡持卡人', 'ok')
    loadSubmissions()
    loadHolders()
  } finally {
    holderSubmitting.value = false
  }
}
async function setHolderStatus(row: Holder, status: 'APPROVED' | 'DISABLED') {
  const word = status === 'DISABLED' ? '停用' : '启用'
  const ok = await dialog.confirm(`${word}持卡人 ${row.card_holder_id}？`, { title: word, okText: word })
  if (!ok) return
  const r = await api(`/cardholders/${encodeURIComponent(row.card_holder_id)}/status`, 'POST', { status })
  if (isUncertain(r)) return dialog.toast('结果未知：请稍后刷新持卡人列表确认', 'warn')
  if (!r.ok) return dialog.toast(errText(r, `${word}失败`), 'err')
  dialog.toast(`已${word}`, 'ok')
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
  const r = await api<Order>(`/orders/${encodeURIComponent(row.out_order_no)}/recheck`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '查询失败'), 'err')
  orderToast('再查一次', r.data)
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

interface Backfill { cursor: number | null; last_run_at: number | null; last_added: number | null; last_error: string }
const backfill = ref<Backfill | null>(null)
const backfilling = ref(false)
async function loadBackfill() {
  const r = await api<Backfill>('/backfill')
  if (r.ok) backfill.value = r.data
}
async function runBackfill(days: number) {
  if (backfilling.value) return
  backfilling.value = true
  try {
    const r = await api<Backfill>('/backfill', 'POST', { days })
    if (!r.ok) {
      dialog.toast(r.status === 0 ? '网络异常，补拉结果未知，请稍后刷新查看' : errText(r, '补拉失败'), r.status === 0 ? 'warn' : 'err')
      loadBackfill()
      return
    }
    backfill.value = r.data
    dialog.toast(`补拉完成，新增 ${r.data.last_added ?? 0} 条`, 'ok')
    loadTxns()
  } finally {
    backfilling.value = false
  }
}

// ---------- 单卡限额 ----------
interface CardLimit { single_limit?: string; daily_limit?: string; monthly_limit?: string }
const limitOpen = ref(false)
const limitCard = ref<Card | null>(null)
const limitLoading = ref(false)
const limitSaving = ref(false)
const limitLoadError = ref('')
const limitError = ref('')
const limitForm = reactive({ single: '', daily: '', monthly: '' })
const limitRemaining = ref<{ daily_remaining?: string; monthly_remaining?: string } | null>(null)

function openLimit(row: Card) {
  limitCard.value = row
  limitOpen.value = true
  limitForm.single = limitForm.daily = limitForm.monthly = ''
  limitRemaining.value = null
  limitError.value = ''
  loadLimit()
}
async function loadLimit() {
  const card = limitCard.value
  if (!card) return
  limitLoading.value = true
  limitLoadError.value = ''
  const r = await api<{ limit?: CardLimit; limit_remaining?: { daily_remaining?: string; monthly_remaining?: string } }>(`/cards/${encodeURIComponent(card.card_id)}/limit-usage`)
  limitLoading.value = false
  if (limitCard.value?.card_id !== card.card_id) return
  if (!r.ok) { limitLoadError.value = errText(r, '读取限额失败'); return }
  limitForm.single = r.data.limit?.single_limit || ''
  limitForm.daily = r.data.limit?.daily_limit || ''
  limitForm.monthly = r.data.limit?.monthly_limit || ''
  limitRemaining.value = r.data.limit_remaining || null
}
async function saveLimit() {
  const card = limitCard.value
  if (!card || limitSaving.value) return
  limitError.value = ''
  const out: Record<string, string> = {}
  const fields: [string, string, string][] = [['single_limit', limitForm.single, '单笔限额'], ['daily_limit', limitForm.daily, '单日限额'], ['monthly_limit', limitForm.monthly, '单月限额']]
  for (const [key, text, name] of fields) {
    if (!text.trim()) continue
    const c = usdToCents(text)
    if (!c.ok) { limitError.value = `${name}：${c.error}`; return }
    out[key] = centsToUsd(c.cents)
  }
  if (!Object.keys(out).length) { limitError.value = '至少填写一项限额'; return }
  const desc = fields.map(([k, , name]) => `${name}：${out[k] ? out[k] + ' 美元' : '不限制'}`).join('\n')
  const ok = await dialog.confirm(
    `卡 ${card.card_id}（尾号 ${card.last4 || '—'}）\n将把限额整体替换为：\n${desc}\n\n设置限额可能导致付款被拒。确认设置？`,
    { title: '确认设置限额', okText: '确认设置', danger: true },
  )
  if (!ok) return
  limitSaving.value = true
  const r = await api(`/cards/${encodeURIComponent(card.card_id)}/limit`, 'PUT', out)
  limitSaving.value = false
  if (r.ok) { dialog.toast('限额已设置'); limitOpen.value = false; return }
  if (r.status === 409) limitError.value = errText(r, '这张卡正在付款中，现在不能改限额')
  else if (r.status === 502 && r.data?.outcome === 'unknown') limitError.value = '平台可能已经改了，结果不明。请重新读取限额确认；内容相同可以再提交一次。'
  else limitError.value = errText(r, '设置失败')
}

// ---------- 开卡与补卡策略（选卡字段在 MarsWallexSelection 页单独保存） ----------
const policy = ref<Policy | null>(null)
const openAmountText = ref('0.00')
const policySaving = ref(false)
const policyError = ref('')
// 三个资金上限在界面上用美元文本编辑，提交时换算成分
const limitText = reactive({ fund: '', wallet: '', daily: '' })

// 目标数表：覆盖全部已知产品（缺的行由 ensureTargetRows 在加载时补上），保存时只保留有值的
const targetRows = computed(() => {
  const p = policy.value
  if (!p) return []
  const ids = new Set<string>([...products.value.map((x) => x.product_id), ...Object.keys(p.product_targets)])
  return [...ids].filter((id) => p.product_targets[id]).map((id) => ({ id, target: p.product_targets[id] }))
})

// 给每个已知产品补一行目标数（0/0），在加载策略或产品之后调用；computed 里不做改动
function ensureTargetRows() {
  const p = policy.value
  if (!p) return
  for (const x of products.value) {
    if (!p.product_targets[x.product_id]) p.product_targets[x.product_id] = { target: 0, daily_limit: 0 }
  }
}

async function loadPolicy(quiet = false) {
  policyError.value = ''
  const r = await api<Policy>('/policy')
  if (!r.ok) return quiet ? undefined : dialog.toast(errText(r, '加载策略失败'), 'err')
  const p = r.data
  p.plan_products = p.plan_products || {}
  p.product_targets = p.product_targets || {}
  p.holder_ids = p.holder_ids || []
  policy.value = p
  ensureTargetRows()
  openAmountText.value = money(p.open_amount_cents)
  limitText.fund = centsToUsd(p.max_fund_cents)
  limitText.wallet = centsToUsd(p.max_wallet_op_cents)
  limitText.daily = centsToUsd(p.daily_fund_cap_cents)
}

async function savePolicy() {
  if (!policy.value) return
  policyError.value = ''
  const amt = openAmountText.value.trim()
  if (!/^\d+(\.\d{1,2})?$/.test(amt)) {
    policyError.value = '开卡金额无效，须为不少于 0 且最多两位小数的数字'
    return
  }
  const fund = usdToCents(limitText.fund)
  const wallet = usdToCents(limitText.wallet)
  const daily = usdToCents(limitText.daily)
  const bad = !fund.ok ? `单笔注资上限：${fund.error}` : !wallet.ok ? `钱包操作上限：${wallet.error}` : !daily.ok ? `单日注资累计上限：${daily.error}` : ''
  if (bad || !fund.ok || !wallet.ok || !daily.ok) {
    policyError.value = bad
    return
  }
  const countCap = policy.value.daily_fund_count_cap
  if (!Number.isInteger(countCap) || countCap < 1) {
    policyError.value = '单日注资笔数上限：须为不小于 1 的整数'
    return
  }
  // 资金上限变了要二次确认：写清旧值和新值
  const old = await api<Policy>('/policy')
  if (!old.ok) {
    policyError.value = '无法读取当前策略来对比资金上限，已取消保存：' + errText(old, '读取失败')
    return
  }
  const changes: string[] = []
  const chk = (name: string, before: number, after: number) => {
    if (before !== after) changes.push(`${name}：${centsToUsd(before)} → ${centsToUsd(after)} 美元`)
  }
  chk('单笔注资上限', old.data.max_fund_cents, fund.cents)
  chk('钱包操作上限', old.data.max_wallet_op_cents, wallet.cents)
  chk('单日注资累计上限', old.data.daily_fund_cap_cents, daily.cents)
  if (old.data.daily_fund_count_cap !== countCap) {
    changes.push(`单日注资笔数上限：${old.data.daily_fund_count_cap} → ${countCap} 笔`)
  }
  if (!!old.data.replenish_when_unselected !== !!policy.value.replenish_when_unselected) {
    changes.push(`未选用 MarsWallex 时自动补卡：${policy.value.replenish_when_unselected ? '关 → 开（会占用主钱包余额开卡）' : '开 → 关'}`)
  }
  if (changes.length) {
    const ok = await dialog.confirm(`将修改影响资金的策略：\n${changes.join('\n')}\n\n确认保存？`, { title: '确认修改资金策略', okText: '确认保存', danger: true })
    if (!ok) return
  }
  // PUT 会替换整份策略：带上从服务器加载的全部字段，只改表单里编辑的值。
  // 选卡字段不带：后端沿用当前值，免得冲掉「MarsWallex → 选卡规则」页刚保存的内容。
  const body = withoutSelection({
    ...policy.value,
    max_fund_cents: fund.cents,
    max_wallet_op_cents: wallet.cents,
    daily_fund_cap_cents: daily.cents,
    open_amount_cents: Math.round(Number(amt) * 100),
    product_targets: Object.fromEntries(
      Object.entries(policy.value.product_targets).filter(([, t]) => t.target > 0 || t.daily_limit > 0),
    ),
  })
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


// ---------- 标签切换 / 生命周期 ----------
function onTab(t: string) {
  if (t === 'overview') loadOverview()
  else if (t === 'cards') loadCards()
  else if (t === 'holders') { loadHolders(); loadSubmissions() }
  else if (t === 'orders') loadOrders()
  else if (t === 'txns') { loadTxns(); loadBackfill() }
  else if (t === 'policy') {
    if (!holders.value.length) loadHolders()
    loadPolicy()
  }
}
watch(tab, onTab)

onMounted(() => {
  loadProducts()
  loadPolicy(true) // 查看卡号、释放的确认文字要读使用次数上限和注销天数
  // 刷新后从 ?tab= 直接落在非概览标签时，加载该标签的数据
  if (tab.value === 'overview') loadOverview()
  else { loadOverview(); onTab(tab.value) }
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
:deep(.mismatch-row td) {
  background: rgba(220, 38, 38, 0.12) !important;
  color: #dc2626;
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
.otp {
  font-size: 22px;
  letter-spacing: 3px;
}
</style>
