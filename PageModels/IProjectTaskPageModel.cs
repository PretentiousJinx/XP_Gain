using CommunityToolkit.Mvvm.Input;
using XP_Gain.Models;

namespace XP_Gain.PageModels
{
    public interface IProjectTaskPageModel
    {
        IAsyncRelayCommand<ProjectTask> NavigateToTaskCommand { get; }
        bool IsBusy { get; }
    }
}