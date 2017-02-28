#!groovy
timestamps {
	def projectName = "${env.JOB_NAME}"

	def ecrRegion = "us-east-1"
	def ecrRegistryUrl = "418054522764.dkr.ecr.${ecrRegion}.amazonaws.com"
	def ecrCredentials = '424a1cdf-ff32-4977-b2ae-970ad420e858'
	def githubCredentials = 'a28b8675-4d2e-44c6-9387-7f0b1ed8af05'

	def rootDirectory = projectName
	def friendlyBuildString = "${env.JOB_NAME} build # ${env.BUILD_NUMBER}"
	def dockerRepoName = projectName.toLowerCase()
	def githubUrl = "git@github.com:ccpgames/${projectName}.git"

	slackSend channel: '#builds', color: 'good', message: "Begin ${friendlyBuildString}"

	node('Master') {
		try {
			def gitRevision
			stage('Pull from Git') {
				dir(rootDirectory) {
					git credentialsId: githubCredentials, url: githubUrl
					sh 'git submodule update --init'
					gitRevision = sh(script: 'git rev-parse HEAD', returnStdout: true).trim()
				}
			}

			def buildImageName = "${dockerRepoName}-build:${gitRevision}"
			stage('Build and test') {
				dir(rootDirectory) {
					sh "docker build -t ${buildImageName} -f ./Build.Dockerfile ."
				}
			}

			def localImageName = "${dockerRepoName}:${gitRevision}"
			stage('Create image') {
				dir(rootDirectory) {
					def buildContainer = sh(script: "docker create ${buildImageName}", returnStdout: true).trim()
					sh 'mkdir -p ./publish'
					sh "docker cp ${buildContainer}:/go/bin/${projectName} ./publish/${projectName}"
					sh "cp Deploy.Dockerfile ./publish/Dockerfile"
					sh "docker rm ${buildContainer}"
					sh "docker build -t ${localImageName} ./publish/"
				}
			}

			def remoteImagePrefix = "${ecrRegistryUrl}/${dockerRepoName}" 
			def remoteImageName = "${remoteImagePrefix}:jenkins-${env.BUILD_NUMBER}"
			stage('ECR upload') {
				dir(rootDirectory) {
					withCredentials([[
						$class: 'AmazonWebServicesCredentialsBinding',
						credentialsId: ecrCredentials,
						accessKeyVariable: 'AWS_ACCESS_KEY_ID',
						secretKeyVariable: 'AWS_SECRET_ACCESS_KEY']]) {

						// login to ecr
						sh "eval \$(aws ecr get-login --region ${ecrRegion})"
						// persistent name
						sh "docker tag ${localImageName} ${remoteImageName}"
						sh "docker push ${remoteImageName}"
						// logout
						sh "docker logout ${ecrRegistryUrl}"
					}
				}
			}

			stage('Cleanup') {
				sh "docker rmi ${remoteImageName} ${localImageName} ${buildImageName}"
			}

			slackSend channel: '#builds', color: 'good', message: "Success: ${friendlyBuildString}\n${env.BUILD_URL}"
		} catch (err) {
			slackSend(
				channel: '#builds',
				color: 'bad',
				message: "FAILURE: ${friendlyBuildString} (${err.message})!\n${env.BUILD_URL}console"
			)
			throw err
		}

	}
}
