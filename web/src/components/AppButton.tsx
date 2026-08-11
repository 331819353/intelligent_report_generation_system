import { Button, type ButtonProps } from 'antd'
import type { ReactNode } from 'react'

export type AppButtonProps = Omit<ButtonProps, 'type' | 'size' | 'htmlType' | 'danger' | 'ghost' | 'shape' | 'variant' | 'color'> & {
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
 * 项目按钮统一入口。底层使用 React 生态的 Ant Design Button，保留现有业务层语义。
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

  return <Button
    {...props}
    htmlType={type}
    type={visualType}
    danger={variant === 'danger'}
    ghost={plain && variant !== 'default'}
    shape={circle ? 'circle' : round ? 'round' : 'default'}
    size={size === 'default' ? 'middle' : size}
    className={['app-button', variantClass, plain ? 'is-plain' : '', className].filter(Boolean).join(' ')}
    disabled={disabled}
  >{children}</Button>
}
