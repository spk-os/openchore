import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import {
  Avatar, CategoryMark, Icon, ICON_NAMES, PERSON_COLORS, SKINS, SkinScope, catFromCategory, isIconName,
  resolveChoreIcon, type Cat, type IconName, type PersonColor, type Skin,
} from '../../design';
import type { User } from '../../types';
// 042→taxonomy：学科词表改由服务端 subjects 表驱动（useSubjects），
// 文案沿用现有 subject_* 词条族（词条缺失回退学科 key，subjects.ts 纯函数）。
import { subjectOptionLabel } from '../../subjects';
import { useSubjects } from '../../hooks/useSubjects';
import styles from './pickers.module.css';

/** UI glyphs that make no sense as a chore or reward icon. */
const NOT_FOR_CHORES = new Set<IconName>(['back', 'chev', 'plus']);
export const CHORE_ICON_NAMES: IconName[] = ICON_NAMES.filter((n) => !NOT_FOR_CHORES.has(n));

export type ChoreCategory = 'required' | 'core' | 'bonus';
export const CHORE_CATEGORIES: ChoreCategory[] = ['required', 'core', 'bonus'];

/** A person's colour key, or undefined for legacy / missing values. */
export function personColor(u: Pick<User, 'color'> | undefined | null): PersonColor | undefined {
  return u?.color && (PERSON_COLORS as readonly string[]).includes(u.color) ? u.color : undefined;
}

/** The chore/reward icon in its well. */
export function IconWell({ icon, cat = 'daily', className }: { icon?: string | null; cat?: Cat; className?: string }) {
  return (
    <span className={clsx(styles.well, className)} aria-hidden>
      <Icon name={resolveChoreIcon(icon, cat)} />
    </span>
  );
}

/* ---------------- IconPicker ---------------- */

export interface IconPickerProps {
  /** The stored `icon`: a line icon name, a legacy emoji, or empty. */
  value: string;
  onChange: (icon: string) => void;
  /** Category for the fallback icon when `value` is empty. */
  cat?: Cat;
  label: string;
}

/**
 * Pick a line icon. Stores the icon *name*; a legacy emoji value keeps
 * working (it resolves to its line icon) until the parent picks another.
 */
export function IconPicker({ value, onChange, cat = 'daily', label }: IconPickerProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const gridId = useId();
  const labelId = useId();
  const current = resolveChoreIcon(value, cat);
  const isName = !!value && isIconName(value.trim().toLowerCase());
  const legacy = !!value.trim() && !isName;

  let summary: string;
  if (!value.trim()) summary = t('admin.iconPicker.automatic', { icon: t(`admin.icons.${current}`) });
  else if (legacy) summary = t('admin.iconPicker.legacy', { value, icon: t(`admin.icons.${current}`) });
  else summary = t(`admin.icons.${current}`);

  return (
    <div className={styles.iconPicker}>
      <span className={styles.label} id={labelId}>{label}</span>
      <div className={styles.iconCurrent}>
        <IconWell icon={value} cat={cat} />
        <span className={styles.iconSummary}>{summary}</span>
        <button
          type="button"
          className={styles.linkBtn}
          aria-expanded={open}
          aria-controls={gridId}
          onClick={() => setOpen((o) => !o)}
        >
          {open ? t('admin.iconPicker.hide') : t('admin.iconPicker.choose')}
        </button>
      </div>
      {open && (
        <div className={styles.iconGrid} id={gridId} role="group" aria-labelledby={labelId}>
          <button
            type="button"
            className={styles.iconOption}
            aria-pressed={!value.trim()}
            aria-label={t('admin.iconPicker.automaticOption')}
            title={t('admin.iconPicker.automaticOption')}
            onClick={() => onChange('')}
          >
            <span className={styles.autoGlyph} aria-hidden>{t('admin.iconPicker.autoShort')}</span>
          </button>
          {CHORE_ICON_NAMES.map((name) => (
            <button
              key={name}
              type="button"
              className={styles.iconOption}
              aria-pressed={isName && current === name}
              aria-label={t(`admin.icons.${name}`)}
              title={t(`admin.icons.${name}`)}
              onClick={() => onChange(name)}
            >
              <Icon name={name} />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/* ---------------- CategoryPicker ---------------- */

export function CategoryPicker({ value, onChange, label }: { value: ChoreCategory; onChange: (c: ChoreCategory) => void; label: string }) {
  const { t } = useTranslation();
  const name = useId();
  return (
    <fieldset className={styles.fieldset}>
      <legend className={styles.label}>{label}</legend>
      <div className={styles.segmented}>
        {CHORE_CATEGORIES.map((c) => {
          const cat = catFromCategory(c);
          return (
            <label key={c} className={styles.segment}>
              <input
                type="radio"
                name={name}
                value={c}
                checked={value === c}
                onChange={() => onChange(c)}
                className={styles.srOnly}
              />
              <CategoryMark cat={cat} />
              <span>{t(`design.category.${cat}`)}</span>
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}

/** A category name with its shape, for lists and reviews. */
export function CategoryLabel({ category }: { category: string }) {
  const { t } = useTranslation();
  const cat = catFromCategory(category);
  return (
    <span className={styles.catLabel}>
      <CategoryMark cat={cat} />
      {t(`design.category.${cat}`)}
    </span>
  );
}

/* ---------------- SubjectPicker ---------------- */

// 042→taxonomy：学科清单改由服务端 subjects 表驱动（useSubjects），不再持有
// 本地静态词表；类型保持开放 string——服务端可增学科，前端不得封死取值域。
export type ChoreSubject = string;

/**
 * The school-subject select for a chore. The subject is what the media
 * library (and the error book) group by, so it shares the
 * admin.analysis.subject_* labels; '' keeps a chore out of every subject
 * bucket. Options come from the server's subjects table (useSubjects) in
 * server order — '' (unclassified) always stays, and there is no static
 * fallback list: while loading or after a failure the select is disabled
 * and a status / error + retry line shows instead of a fake vocabulary.
 */
export function SubjectPicker({ value, onChange, label }: { value: string; onChange: (s: ChoreSubject) => void; label: string }) {
  const { t } = useTranslation();
  const { subjects, loading, error, retry } = useSubjects();
  const selectId = useId();

  // 服务端顺序；''（未分类）始终在列。value 不在服务端清单时也保留为选项：
  // 受控 select 的显示必须与 value 一致，绝不能静默显示成「未分类」。
  // R2：文案优先服务端 SubjectRow.name（subjectOptionLabel 第 4 参），
  // 缺 name 再走 subject_* 词条 / 原始 key。
  const byId = new Map(subjects.map((row) => [row.id, row.name]));
  const serverIds = subjects.map((row) => row.id).filter((id) => id !== '');
  const options = value && !serverIds.includes(value) ? ['', ...serverIds, value] : ['', ...serverIds];

  // 状态行放 <label> 外：label 的隐式控件是 select，里面再嵌 button 会把
  // 可点击目标也拉进 label 的激活区（点击歧义 + 可访问性问题）。
  return (
    <div className={styles.fieldset}>
      <label className={styles.subjectLabelBlock} htmlFor={selectId}>
        <span className={styles.label}>{label}</span>
      </label>
      <select
        id={selectId}
        className={styles.subjectSelect}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={loading || error != null}
        data-test="chore-subject-select"
      >
        {options.map((s) => (
          <option key={s || 'none'} value={s}>
            {subjectOptionLabel(t, s, 'admin.editChore.subjectNone', byId.get(s))}
          </option>
        ))}
      </select>
      {loading && (
        <span data-test="chore-subject-loading" role="status">{t('common.loading')}</span>
      )}
      {error != null && (
        <span data-test="chore-subject-error" role="alert">
          {error instanceof Error ? error.message : String(error)}
          {' '}
          <button type="button" className={styles.linkBtn} data-test="chore-subject-retry" onClick={retry}>
            {t('admin.kidsStatusTab.refreshLabel')}
          </button>
        </span>
      )}
    </div>
  );
}


/* ---------------- People ---------------- */

/** A toggle chip for a person: avatar in their colour + name. */
export function PersonToggle({ user, pressed, onClick }: { user: User; pressed: boolean; onClick: () => void }) {
  return (
    <button type="button" className={styles.personChip} aria-pressed={pressed} onClick={onClick}>
      <Avatar name={user.name} color={personColor(user)} size="sm" />
      {user.name}
    </button>
  );
}

/** A person's avatar + name, inline. */
export function PersonName({ user, name, size = 'sm' }: { user?: User | null; name?: string; size?: 'sm' | 'md' }) {
  const display = user?.name ?? name ?? '';
  return (
    <span className={styles.person} data-person={personColor(user)}>
      <Avatar name={display} color={personColor(user)} size={size} />
      <span className={styles.personName}>{display}</span>
    </span>
  );
}

/* ---------------- Skin + colour ---------------- */

export function SkinPicker({ value, onChange, color, name, label }: {
  value: Skin;
  onChange: (s: Skin) => void;
  color?: PersonColor;
  name: string;
  label: string;
}) {
  const { t } = useTranslation();
  const group = useId();
  return (
    <fieldset className={styles.fieldset}>
      <legend className={styles.label}>{label}</legend>
      <div className={styles.doors}>
        {SKINS.map((skin) => (
          <label key={skin} className={styles.doorOption}>
            <input
              type="radio"
              name={group}
              value={skin}
              checked={value === skin}
              onChange={() => onChange(skin)}
              className={styles.srOnly}
            />
            <SkinScope skin={skin} color={color} door className={styles.door} aria-hidden>
              <Avatar name={name || '?'} color={color} size="md" />
              <span className={styles.doorMarks}>
                <CategoryMark cat="essential" done />
                <CategoryMark cat="daily" done />
                <CategoryMark cat="bonus" />
              </span>
              <span className={styles.doorName}>{t(`admin.skins.${skin}.name`)}</span>
            </SkinScope>
            {value === skin && (
              <span className={styles.doorCheck} aria-hidden><Icon name="check" /></span>
            )}
            <span className={styles.doorCaption}>
              <span className={styles.doorCaptionName}>{t(`admin.skins.${skin}.name`)}</span>
              <span className={styles.doorCaptionDesc}>{t(`admin.skins.${skin}.desc`)}</span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

export function ColorPicker({ value, onChange, label }: { value?: PersonColor; onChange: (c: PersonColor) => void; label: string }) {
  const { t } = useTranslation();
  const group = useId();
  return (
    <fieldset className={styles.fieldset}>
      <legend className={styles.label}>{label}</legend>
      <div className={styles.swatches}>
        {PERSON_COLORS.map((c) => (
          <label key={c} className={styles.swatchOption} title={t(`admin.colors.${c}`)}>
            <input
              type="radio"
              name={group}
              value={c}
              checked={value === c}
              onChange={() => onChange(c)}
              className={styles.srOnly}
              aria-label={t(`admin.colors.${c}`)}
            />
            <span className={styles.swatch} data-person={c} aria-hidden>
              {value === c && <Icon name="check" />}
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** Screen-reader-only helper shared by the admin screens. */
export const srOnly = styles.srOnly;

