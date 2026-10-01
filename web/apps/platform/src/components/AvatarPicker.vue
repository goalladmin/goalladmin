<script setup lang="ts">
// 更换头像（D-040）：上传自己的图片（先在这里裁成正方形）或者选一个内置头像，也可以恢复成首字母。
// 浏览器把裁好的图画成 512×512 的 JPEG 再上传；服务器还会重新解码、缩放、重新编码，这里的裁剪只是为了好看。
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Upload } from '@element-plus/icons-vue'
import { AVATAR_PRESETS, GaAvatar, describeApiError, isApiError, presetAvatarSrc, useAuthStore, useI18n } from '@ga/shell'

import { profileApi } from '../api/system'

const props = defineProps<{ modelValue: boolean; current: string; username: string }>()
const emit = defineEmits<{ 'update:modelValue': [boolean]; changed: [string] }>()

const { t, te } = useI18n()
const auth = useAuthStore()
// 关闭对话框、组件卸载时加一：之前开始、还在等裁剪的保存作废（D-058）
let generation = 0
onBeforeUnmount(() => generation++)
const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v),
})

type Tab = 'upload' | 'preset'
const tab = ref<Tab>('upload')
const saving = ref(false)
const error = ref('')
const preset = ref('')

// ---- 裁剪 ----
const VIEW = 280 // 裁剪框的边长（像素）
const OUT = 512 // 上传的图片边长
const MAX_FILE = 10 * 1024 * 1024 // 选择的文件上限（浏览器解码前先挡掉特别大的）
const canvas = ref<HTMLCanvasElement>()
const fileInput = ref<HTMLInputElement>()
const img = ref<HTMLImageElement | null>(null)
const crop = reactive({ zoom: 1, x: 0, y: 0 }) // 图片左上角在裁剪框里的位置（按裁剪框像素）
let drag: { px: number; py: number; x: number; y: number } | null = null
let loadSeq = 0

// 图片在 zoom=1 时刚好铺满裁剪框（短边等于框边）
const baseScale = computed(() => (img.value ? VIEW / Math.min(img.value.naturalWidth, img.value.naturalHeight) : 1))

function clamp() {
  if (!img.value) return
  const s = baseScale.value * crop.zoom
  const w = img.value.naturalWidth * s
  const h = img.value.naturalHeight * s
  crop.x = Math.min(0, Math.max(VIEW - w, crop.x))
  crop.y = Math.min(0, Math.max(VIEW - h, crop.y))
}

function draw() {
  const c = canvas.value
  if (!c) return
  const ctx = c.getContext('2d')
  if (!ctx) return
  ctx.clearRect(0, 0, VIEW, VIEW)
  if (!img.value) return
  const s = baseScale.value * crop.zoom
  ctx.drawImage(img.value, crop.x, crop.y, img.value.naturalWidth * s, img.value.naturalHeight * s)
}

watch(
  () => [crop.zoom, crop.x, crop.y, img.value] as const,
  () => {
    clamp()
    draw()
  },
)

function setZoom(z: number) {
  // 以裁剪框中心为基准缩放
  const old = crop.zoom
  const cx = VIEW / 2
  crop.x = cx - ((cx - crop.x) * z) / old
  crop.y = cx - ((cx - crop.y) * z) / old
  crop.zoom = z
}

function pickFile() {
  error.value = ''
  fileInput.value?.click()
}

function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (!file.type.startsWith('image/')) {
    error.value = t('profile.avatar.badType')
    return
  }
  if (file.size > MAX_FILE) {
    error.value = t('profile.avatar.tooLarge')
    return
  }
  // 预览用 data: 地址而不是 blob: 地址：部署示例的 CSP 只放行 img-src 'self' data:（D-043），
  // 用 blob: 会被挡掉、选了图什么都不显示
  const seq = ++loadSeq // 连续选了两张：只认最后一张
  const reader = new FileReader()
  reader.onload = () => {
    if (seq !== loadSeq) return
    const el = new Image()
    el.onload = async () => {
      if (seq !== loadSeq) return
      img.value = el
      crop.zoom = 1
      const s = baseScale.value
      crop.x = (VIEW - el.naturalWidth * s) / 2
      crop.y = (VIEW - el.naturalHeight * s) / 2
      await nextTick()
      draw()
    }
    el.onerror = () => {
      if (seq !== loadSeq) return
      img.value = null
      error.value = t('profile.avatar.badType')
    }
    el.src = reader.result as string
  }
  reader.onerror = () => {
    if (seq !== loadSeq) return
    img.value = null
    error.value = t('profile.avatar.badType')
  }
  reader.readAsDataURL(file)
}

function onPointerDown(e: PointerEvent) {
  if (!img.value) return
  ;(e.target as HTMLElement).setPointerCapture(e.pointerId)
  drag = { px: e.clientX, py: e.clientY, x: crop.x, y: crop.y }
}
function onPointerMove(e: PointerEvent) {
  if (!drag) return
  crop.x = drag.x + (e.clientX - drag.px)
  crop.y = drag.y + (e.clientY - drag.py)
}
function onPointerUp() {
  drag = null
}
function onWheel(e: WheelEvent) {
  if (!img.value) return
  setZoom(Math.min(4, Math.max(1, crop.zoom * (e.deltaY < 0 ? 1.08 : 1 / 1.08))))
}

function cropped(): Promise<Blob | null> {
  const el = img.value
  if (!el) return Promise.resolve(null)
  const out = document.createElement('canvas')
  out.width = OUT
  out.height = OUT
  const ctx = out.getContext('2d')
  if (!ctx) return Promise.resolve(null)
  ctx.fillStyle = '#fff'
  ctx.fillRect(0, 0, OUT, OUT)
  const k = OUT / VIEW
  const s = baseScale.value * crop.zoom * k
  ctx.drawImage(el, crop.x * k, crop.y * k, el.naturalWidth * s, el.naturalHeight * s)
  return new Promise((resolve) => out.toBlob((b) => resolve(b), 'image/jpeg', 0.9))
}

function explain(e: unknown): string {
  if (isApiError(e)) return describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  return t('shell.error.network')
}

async function run(fn: () => Promise<{ avatar: string }>) {
  saving.value = true
  error.value = ''
  try {
    const r = await fn()
    emit('changed', r.avatar)
    visible.value = false
    ElMessage.success(t('profile.avatar.saved'))
  } catch (e) {
    error.value = explain(e)
  } finally {
    saving.value = false
  }
}

async function save() {
  if (tab.value === 'preset') {
    if (!preset.value) return
    await run(() => profileApi.setPresetAvatar(preset.value))
    return
  }
  // 裁剪是异步的：等待期间关了对话框、退出或换了账号，这次保存作废，不能用新登录的身份把图传上去（D-058）
  const gen = generation
  const started = auth.epoch
  const blob = await cropped()
  if (gen !== generation || auth.epoch !== started) return
  if (!blob) {
    error.value = t('profile.avatar.pickFirst')
    return
  }
  await run(() => profileApi.uploadAvatar(blob))
}

async function clear() {
  await run(() => profileApi.clearAvatar())
}

const canSave = computed(() => (tab.value === 'preset' ? !!preset.value : !!img.value))

// 每次打开：回到上传页，选中当前的内置头像（如果是）
watch(visible, (v) => {
  if (!v) {
    generation++
    return
  }
  error.value = ''
  // 每次打开都从头开始：上一次选的图和裁剪位置不留着
  loadSeq++
  img.value = null
  Object.assign(crop, { zoom: 1, x: 0, y: 0 })
  preset.value = props.current.startsWith('preset:') ? props.current.slice(7) : ''
  tab.value = preset.value ? 'preset' : 'upload'
})
</script>

<template>
  <el-dialog v-model="visible" :title="t('profile.avatar.title')" width="460px" data-test="avatar-dialog" append-to-body>
    <el-tabs v-model="tab">
      <el-tab-pane :label="t('profile.avatar.tabUpload')" name="upload">
        <div class="ga-avatar-pick__crop">
          <div
            class="ga-avatar-pick__frame"
            :class="{ 'is-empty': !img }"
            @pointerdown="onPointerDown"
            @pointermove="onPointerMove"
            @pointerup="onPointerUp"
            @pointercancel="onPointerUp"
            @wheel.prevent="onWheel"
          >
            <canvas ref="canvas" :width="VIEW" :height="VIEW" />
            <div v-if="!img" class="ga-avatar-pick__placeholder">
              <GaAvatar :size="96" :value="current" :name="username" />
              <span>{{ t('profile.avatar.hint') }}</span>
            </div>
            <div v-else class="ga-avatar-pick__mask" />
          </div>
          <el-slider
            v-if="img"
            :model-value="crop.zoom"
            :min="1"
            :max="4"
            :step="0.01"
            :show-tooltip="false"
            class="ga-avatar-pick__zoom"
            :aria-label="t('profile.avatar.zoom')"
            @update:model-value="(v: number | number[]) => setZoom(Array.isArray(v) ? v[0]! : v)"
          />
          <input ref="fileInput" type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden data-test="avatar-file" @change="onFile" />
          <el-button :icon="Upload" data-test="avatar-pick-file" @click="pickFile">{{ img ? t('profile.avatar.another') : t('profile.avatar.choose') }}</el-button>
          <div class="ga-avatar-pick__note">{{ t('profile.avatar.note') }}</div>
        </div>
      </el-tab-pane>
      <el-tab-pane :label="t('profile.avatar.tabPreset')" name="preset">
        <div class="ga-avatar-pick__grid" role="radiogroup">
          <button
            v-for="p in AVATAR_PRESETS"
            :key="p.name"
            type="button"
            role="radio"
            :aria-checked="preset === p.name"
            class="ga-avatar-pick__preset"
            :class="{ 'is-active': preset === p.name }"
            :data-test="`avatar-preset-${p.name}`"
            @click="preset = p.name"
          >
            <img :src="presetAvatarSrc(p.name)" alt="" />
          </button>
        </div>
      </el-tab-pane>
    </el-tabs>
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon data-test="avatar-error" />
    <template #footer>
      <el-button v-if="current" link type="danger" class="ga-avatar-pick__clear" :loading="saving" data-test="avatar-clear" @click="clear">{{ t('profile.avatar.clear') }}</el-button>
      <el-button @click="visible = false">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :disabled="!canSave" :loading="saving" data-test="avatar-save" @click="save">{{ t('common.save') }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.ga-avatar-pick__crop {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.ga-avatar-pick__frame {
  position: relative;
  width: 280px;
  height: 280px;
  border-radius: var(--ga-radius);
  overflow: hidden;
  background: var(--el-fill-color-light);
  cursor: grab;
  touch-action: none;
}

.ga-avatar-pick__frame.is-empty {
  cursor: default;
}

.ga-avatar-pick__frame canvas {
  display: block;
}

/* 圆形取景：圆外压暗，头像最终按圆形显示 */
.ga-avatar-pick__mask {
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: radial-gradient(circle at center, transparent 139px, rgb(0 0 0 / 45%) 140px);
}

.ga-avatar-pick__placeholder {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--ga-text-secondary);
  font-size: 13px;
  text-align: center;
  padding: 0 24px;
}

.ga-avatar-pick__zoom {
  width: 240px;
}

.ga-avatar-pick__note {
  color: var(--ga-text-secondary);
  font-size: 12px;
  text-align: center;
}

.ga-avatar-pick__grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  padding: 4px;
}

.ga-avatar-pick__preset {
  aspect-ratio: 1;
  padding: 0;
  border: 3px solid transparent;
  border-radius: 50%;
  background: none;
  cursor: pointer;
  transition:
    transform 0.15s,
    border-color 0.15s;
}

.ga-avatar-pick__preset img {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: 50%;
}

.ga-avatar-pick__preset:hover {
  transform: scale(1.05);
}

.ga-avatar-pick__preset.is-active {
  border-color: var(--ga-primary);
}

.ga-avatar-pick__clear {
  float: left;
  margin-top: 8px;
}
</style>
