import { zodResolver } from '@hookform/resolvers/zod'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom'
import { ApiError } from '../api/client'
import { Button, Card, Field, Input, Spinner } from '../components/ui'
import { useAuth } from '../context/auth'
import { credentialsSchema, type CredentialsForm } from '../utils/schemas'

function AuthForm({ mode }: { mode: 'login' | 'register' }) {
  const { user, loading, login, register: registerUser } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [serverError, setServerError] = useState<string | null>(null)
  const from = (location.state as { from?: string } | null)?.from ?? '/'
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<CredentialsForm>({ resolver: zodResolver(credentialsSchema) })

  if (loading) return <Spinner />
  if (user) return <Navigate to={from} replace />

  const isLogin = mode === 'login'
  const onSubmit = handleSubmit(async ({ email, password }) => {
    setServerError(null)
    try {
      await (isLogin ? login(email, password) : registerUser(email, password))
      navigate(from, { replace: true })
    } catch (e) {
      setServerError(e instanceof ApiError ? e.message : 'Could not reach the server. Try again.')
    }
  })

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="w-full max-w-sm space-y-4">
        <h1 className="text-center text-2xl font-semibold tracking-tight">EventMesh</h1>
        <Card title={isLogin ? 'Sign in' : 'Create your account'}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <Field label="Email" htmlFor="email" error={errors.email?.message}>
              <Input id="email" type="email" autoComplete="email" {...register('email')} />
            </Field>
            <Field
              label="Password"
              htmlFor="password"
              error={errors.password?.message}
              hint={isLogin ? undefined : 'At least 10 characters'}
            >
              <Input
                id="password"
                type="password"
                autoComplete={isLogin ? 'current-password' : 'new-password'}
                {...register('password')}
              />
            </Field>
            {serverError && (
              <p role="alert" className="text-sm text-bad">
                {serverError}
              </p>
            )}
            <Button type="submit" variant="primary" className="w-full" disabled={isSubmitting}>
              {isLogin ? 'Sign in' : 'Create account'}
            </Button>
          </form>
        </Card>
        <p className="text-center text-sm text-muted">
          {isLogin ? (
            <>
              No account?{' '}
              <Link className="text-accent underline" to="/register" state={location.state}>
                Create one
              </Link>
            </>
          ) : (
            <>
              Already registered?{' '}
              <Link className="text-accent underline" to="/login" state={location.state}>
                Sign in
              </Link>
            </>
          )}
        </p>
      </div>
    </div>
  )
}

export const LoginPage = () => <AuthForm mode="login" />
export const RegisterPage = () => <AuthForm mode="register" />
