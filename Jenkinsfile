// CI/CD for the resume screener.
// Jenkins needs: Docker on the agent, and an SSH credential "vps-deploy" for the server.
// Set DEPLOY_HOST (user@host) and DEPLOY_PATH (repo folder on the server) as job
// parameters or global environment variables.
pipeline {
    agent any

    options {
        timestamps()
        disableConcurrentBuilds()
        timeout(time: 30, unit: 'MINUTES')
    }

    parameters {
        string(name: 'DEPLOY_HOST', defaultValue: '', description: 'SSH target, e.g. deploy@203.0.113.10')
        string(name: 'DEPLOY_PATH', defaultValue: '/srv/resume-screener-go', description: 'Repo folder on the server')
        string(name: 'HEALTH_URL', defaultValue: 'https://hire.chetanmeniya.dev/healthz', description: 'Checked after deploy')
    }

    stages {
        stage('Test') {
            steps {
                // vet + tests with the race detector, in a throwaway Go container
                sh '''
                    docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false golang:1.24 \
                        sh -c "go vet ./... && go test -race -count=1 ./..."
                '''
            }
        }

        stage('Build image') {
            steps {
                sh 'docker build -f docker/Dockerfile -t resume-screener-go:${GIT_COMMIT} .'
            }
        }

        stage('Deploy') {
            when { branch 'main' }
            steps {
                sshagent(credentials: ['vps-deploy']) {
                    sh '''
                        test -n "$DEPLOY_HOST" || { echo "DEPLOY_HOST is not set"; exit 1; }
                        ssh -o StrictHostKeyChecking=accept-new "$DEPLOY_HOST" "
                            set -e
                            cd '$DEPLOY_PATH'
                            git fetch --quiet origin main
                            git reset --hard origin/main
                            docker compose up -d --build  # compose files come from COMPOSE_FILE in the server .env
                            docker image prune -f
                        "
                    '''
                }
            }
        }

        stage('Health check') {
            when { branch 'main' }
            steps {
                retry(10) {
                    sleep(time: 6, unit: 'SECONDS')
                    sh 'curl -fsS "$HEALTH_URL"'
                }
            }
        }
    }

    post {
        always { sh 'docker image rm resume-screener-go:${GIT_COMMIT} || true' }
    }
}
