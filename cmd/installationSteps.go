package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"os/exec"
	"syscall"
	"time"

	"net/url"
	"net/http"
	"encoding/json"

	"github.com/google/uuid"
	"bytes"
	"encoding/base64"
)

func applyBaseTerraform(cmd *cobra.Command,directory string){
	applyBase := viper.GetBool("create.terraformapplied.base")
	if applyBase != true {
		log.Println("Executing ApplyBaseTerraform")
		if dryrunMode {
			log.Printf("[#99] Dry-run mode, applyBaseTerraform skipped.")
			return
		}		
		terraformAction := "apply"

		os.Setenv("TF_VAR_aws_account_id", viper.GetString("aws.accountid"))
		os.Setenv("TF_VAR_aws_region", viper.GetString("aws.region"))
		os.Setenv("TF_VAR_hosted_zone_name", viper.GetString("aws.domainname"))

		err := os.Chdir(directory)
		if err != nil {
			log.Panicf("error changing dir")
		}

		viperDestoryFlag := viper.GetBool("terraform.destroy")
		cmdDestroyFlag, _ := cmd.Flags().GetBool("destroy")

		if viperDestoryFlag == true || cmdDestroyFlag == true {
			terraformAction = "destroy"
		}

		log.Println("terraform action: ", terraformAction, "destroyFlag: ", viperDestoryFlag)
		execShellReturnStrings(terraformPath, "init")
		execShellReturnStrings(terraformPath, fmt.Sprintf("%s", terraformAction), "-auto-approve")
		keyOut, _, errKey := execShellReturnStrings(terraformPath, "output", "vault_unseal_kms_key")
		if errKey != nil {
			log.Panicf("failed to call tfOutputCmd.Run(): ", err)
		}
		keyId := strings.TrimSpace(keyOut)
		log.Println("keyid is:", keyId)
		viper.Set("vault.kmskeyid", keyId)
		viper.Set("create.terraformapplied.base", true)
		viper.WriteConfig()
		detokenize(fmt.Sprintf("%s/.kubefirst/gitops", home))
	} else {
		log.Println("Skipping: ApplyBaseTerraform")
	}
}


func applyGitlabTerraform(directory string){
	if !viper.GetBool("create.terraformapplied.gitlab") {
		log.Println("Executing applyGitlabTerraform")
		if dryrunMode {
			log.Printf("[#99] Dry-run mode, applyGitlabTerraform skipped.")
			return
		}		
		// Prepare for terraform gitlab execution
		os.Setenv("GITLAB_TOKEN", viper.GetString("gitlab.token"))
		os.Setenv("GITLAB_BASE_URL", fmt.Sprintf("https://gitlab.%s", viper.GetString("aws.domainname")))

		directory = fmt.Sprintf("%s/.kubefirst/gitops/terraform/gitlab", home)
		err := os.Chdir(directory)
		if err != nil {
			log.Println("error changing dir")
		}
		execShellReturnStrings(terraformPath, "init")
		execShellReturnStrings(terraformPath, "apply", "-auto-approve")
		viper.Set("create.terraformapplied.gitlab", true)
		viper.WriteConfig()
	} else {
		log.Println("Skipping: applyGitlabTerraform")
	}
}

func configureSoftserveAndPush(){
	configureAndPushFlag := viper.GetBool("create.softserve.configure")
	if configureAndPushFlag != true {
		log.Println("Executing configureSoftserveAndPush")
		if dryrunMode {
			log.Printf("[#99] Dry-run mode, configureSoftserveAndPush skipped.")
			return
		}		
		kPortForward := exec.Command(kubectlClientPath, "--kubeconfig", kubeconfigPath, "-n", "soft-serve", "port-forward", "svc/soft-serve", "8022:22")
		kPortForward.Stdout = os.Stdout
		kPortForward.Stderr = os.Stderr
		err := kPortForward.Start()
		defer kPortForward.Process.Signal(syscall.SIGTERM)
		if err != nil {
			log.Println("failed to call kPortForward.Run(): ", err)
		}
		time.Sleep(10 * time.Second)

		configureSoftServe()
		pushGitopsToSoftServe()
		viper.Set("create.softserve.configure", true)
		viper.WriteConfig()
		time.Sleep(10 * time.Second)
	} else {
		log.Println("Skipping: configureSoftserveAndPush")
	}
}

func gitlabKeyUpload(){
	// upload ssh public key	
	if !viper.GetBool("gitlab.keyuploaded") {
		log.Println("Executing gitlabKeyUpload")
		if dryrunMode {
			log.Printf("[#99] Dry-run mode, gitlabKeyUpload skipped.")
			return
		}		
		log.Println("uploading ssh public key to gitlab")
		gitlabToken := viper.GetString("gitlab.token")
		data := url.Values{
			"title": {"kubefirst"},
			"key":   {viper.GetString("botpublickey")},
		}

		gitlabUrlBase := fmt.Sprintf("https://gitlab.%s", viper.GetString("aws.domainname"))

		resp, err := http.PostForm(gitlabUrlBase+"/api/v4/user/keys?private_token="+gitlabToken, data)
		if err != nil {
			log.Fatal(err)
		}
		var res map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&res)
		log.Println(res)
		log.Println("ssh public key uploaded to gitlab")
		viper.Set("gitlab.keyuploaded", true)
		viper.WriteConfig()
	} else {
		log.Println("Skipping: gitlabKeyUpload")
		log.Println("ssh public key already uploaded to gitlab")
	}
}


func produceGitlabTokens(){
	//TODO: Should this step be skipped if already executed?
	log.Println("discovering gitlab toolbox pod")	
	if dryrunMode {
		log.Printf("[#99] Dry-run mode, produceGitlabTokens skipped.")
		return
	}
	var outb, errb bytes.Buffer
	k := exec.Command(kubectlClientPath, "--kubeconfig", kubeconfigPath, "-n", "gitlab", "get", "pod", "-lapp=toolbox", "-o", "jsonpath='{.items[0].metadata.name}'")
	k.Stdout = &outb
	k.Stderr = &errb
	err := k.Run()
	if err != nil {
		log.Println("failed to call k.Run() to get gitlab pod: ", err)
	}
	gitlabPodName := outb.String()
	gitlabPodName = strings.Replace(gitlabPodName, "'", "", -1)
	log.Println("gitlab pod", gitlabPodName)

	gitlabToken := viper.GetString("gitlab.token")
	if gitlabToken == "" {

		log.Println("getting gitlab personal access token")

		id := uuid.New()
		gitlabToken = id.String()[:20]

		k = exec.Command(kubectlClientPath, "--kubeconfig", kubeconfigPath, "-n", "gitlab", "exec", gitlabPodName, "--", "gitlab-rails", "runner", fmt.Sprintf("token = User.find_by_username('root').personal_access_tokens.create(scopes: [:write_registry, :write_repository, :api], name: 'Automation token'); token.set_token('%s'); token.save!", gitlabToken))
		k.Stdout = os.Stdout
		k.Stderr = os.Stderr
		err = k.Run()
		if err != nil {
			log.Println("failed to call k.Run() to set gitlab token: ", err)
		}

		viper.Set("gitlab.token", gitlabToken)
		viper.WriteConfig()

		log.Println("gitlabToken", gitlabToken)
	}

	gitlabRunnerToken := viper.GetString("gitlab.runnertoken")
	if gitlabRunnerToken == "" {

		log.Println("getting gitlab runner token")

		var tokenOut, tokenErr bytes.Buffer
		k = exec.Command(kubectlClientPath, "--kubeconfig", kubeconfigPath, "-n", "gitlab", "get", "secret", "gitlab-gitlab-runner-secret", "-o", "jsonpath='{.data.runner-registration-token}'")
		k.Stdout = &tokenOut
		k.Stderr = &tokenErr
		err = k.Run()
		if err != nil {
			log.Println("failed to call k.Run() to get gitlabRunnerRegistrationToken: ", err)
		}
		encodedToken := tokenOut.String()
		log.Println(encodedToken)
		encodedToken = strings.Replace(encodedToken, "'", "", -1)
		log.Println(encodedToken)
		gitlabRunnerRegistrationTokenBytes, err := base64.StdEncoding.DecodeString(encodedToken)
		gitlabRunnerRegistrationToken := string(gitlabRunnerRegistrationTokenBytes)
		log.Println(gitlabRunnerRegistrationToken)
		if err != nil {
			panic(err)
		}
		viper.Set("gitlab.runnertoken", gitlabRunnerRegistrationToken)
		viper.WriteConfig()
		log.Println("gitlabRunnerRegistrationToken", gitlabRunnerRegistrationToken)
	}

}