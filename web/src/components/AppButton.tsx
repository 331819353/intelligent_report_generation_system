import type { ButtonHTMLAttributes, ReactNode } from 'react'

export type AppButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type'> & {
  type?: 'button' | 'submit' | 'reset'
  variant?: 'default' | 'primary' | 'success' | 'warning' | 'danger' | 'info'
  size?: 'default' | 'small' | 'large'
  plain?: boolean
  text?: boolean
  link?: boolean
  round?: boolean
  circle?: boolean
  children?: ReactNode
}

/**
 * 项目按钮统一入口。使用原生 button 与项目 CSS，避免第三方运行时样式影响页面布局。
 */
export function AppButton({
  variant = 'default',
  size = 'default',
  plain = false,
  text = false,
  link = false,
  round = false,
  circle = false,
  className = '',
  disabled,
  children,
  type = 'button',
  ...props
}: AppButtonProps) {
  const visualType = link ? 'link' : text ? 'text' : variant === 'default' || plain ? 'default' : 'primary'
  const variantClass = variant !== 'default' && variant !== 'primary' ? `app-button--${variant}` : ''
  const visualClass = `is-${visualType}`
  const sizeClass = `is-${size}`

  return <button
    {...props}
    type={type}
    className={[
      'app-button',
      visualClass,
      sizeClass,
      variantClass,
      plain ? 'is-plain' : '',
      round ? 'is-round' : '',
      circle ? 'is-circle' : '',
      className,
    ].filter(Boolean).join(' ')}
    disabled={disabled}
  >{children}</button>
}
